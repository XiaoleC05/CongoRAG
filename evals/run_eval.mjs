#!/usr/bin/env node
// run_eval.mjs —— 端到端 eval 的驱动。
//
// 它只做一件事：把 evals/corpus/ 灌进一个真的知识库，用 evals/dataset.jsonl
// 里的问题逐条问一遍，把 SSE 流里的 citation / token 收下来，交给
// evals/lib/score.mjs 打分，写一份结果文件。
//
// 【它不碰数据库】所有判据都来自产品面：文档状态走 GET /documents/{id}，
// 检索结果走 citation 事件里的 snippet（服务端填的是分块的完整正文）。
// 走界面而不是走 psql 的原因很简单——绕开产品的测量方法测不出产品的
// 行为，而且那意味着要把数据库连接串交给一个测量脚本。
// （measure_recall.sh 是刻意的例外：它测的是 HNSW 相对精确扫描的召回率，
// 那件事只能在数据库里做。两者的分母不一样，不能混着看。）
//
// 【为什么这个脚本不进 CI】它要真的起栈、真的调 embedding 和 chat、
// 真的花钱，而且回答那一段没有固定温度，同一个问题两次跑结果就不一样。
// 一个随机变红的门禁比没有门禁更糟：它会训练人忽略红色。CI 里跑的是
// evals/lib/ 下的单测——纯算术和纯解析，输入是手写的帧。
//
// 用法见 evals/README.md，或者 node evals/run_eval.mjs --help

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import {
  loadCorpus,
  loadDataset,
  validateDataset,
  resolveAnchors,
  datasetStats,
  corpusHash,
  corpusChunkEstimate,
  defaultCorpusDir,
  defaultDatasetPath,
} from "./lib/resolve.mjs";

import {
  newParserState,
  parseSSE,
  citationsOf,
  answerOf,
  errorOf,
  scoreQuestion,
  summarise,
  aggregatePasses,
  compareReports,
  buildReport,
  formatReport,
  formatDiff,
  metricLabel,
  DEFAULT_K_VALUES,
  sha256Hex,
} from "./lib/score.mjs";

const EVAL_KB_NAME = "__eval__";
const EVAL_CONV_TITLE = "__eval__";

// 退出码。分数高低不改退出码——这是刻意的：一个"分数掉到 0.7 以下就红"
// 的门禁在没有固定温度的今天只会制造噪声。只有"这次跑的数字根本不成立"
// 才算失败。
//
// 每一类前置条件给一个独立的码，这样脚本外面能一眼看出是"环境没起"
// 还是"语料变了"——两者的处置方式完全不同。
const EXIT = {
  OK: 0,
  USAGE: 64, // 参数或环境变量不对
  CORPUS: 65, // 语料/数据集对不上、指纹不匹配、基线用的是另一份语料
  STACK: 66, // /healthz 不通，或者服务端返回了意料之外的错误
  MODEL: 67, // embedding 模型不匹配，或者服务端还没有 embedding 模型
  DOCUMENT: 68, // 文档处理 failed，或者等 ready 超时
  INTERNAL: 70,
};

const HELP = `用法: node evals/run_eval.mjs [选项]

把 evals/corpus/ 灌进一个真的知识库，用 evals/dataset.jsonl 逐条提问，
把 citation / token 收下来打分，结果写到 evals/results/。

需要的环境变量
  CONGORAG_API_BASE_URL   API 地址，默认 http://127.0.0.1:3210（必须是回环地址，
                          服务端只接受 localhost / 127.0.0.1）
  CONGORAG_EMBED_MODEL    当前生效的 embedding 模型名。必须和
                          GET /api/v1/providers 里最新的那个 embedding 模型一致，
                          不一致就直接退出——模型换了而语料是按旧模型建的，
                          整张表都没有意义。
  CONGORAG_CHAT_BASE_URL  \\ 只有 --judge 用；默认关闭。
  CONGORAG_CHAT_API_KEY   |  OpenAI 兼容的 chat completions 端点。
  CONGORAG_CHAT_MODEL     /

跑之前先起三样东西（三个终端）：
  make up            # PostgreSQL
  make dev           # api，:3210
  make dev-worker    # 文档处理的消费端，没有它文档永远停在 queued

选项
  --api-base URL          覆盖 CONGORAG_API_BASE_URL
  --corpus DIR            语料目录，默认 evals/corpus/
  --dataset FILE          数据集，默认 evals/dataset.jsonl
  --results-dir DIR       结果目录，默认 evals/results/
  --baseline FILE         基线，默认 evals/baseline.json
  --diff [A [B]]          给一个参数：本次运行与该文件对比（不带 --diff 时
                          默认就是跟 evals/baseline.json 对比）；
                          给两个参数：只对比这两个已存在的报告文件，不跑栈
  --write-baseline        把本次结果写进 --baseline 指定的文件
  --repeat N              整套问题连跑 N 遍，生成指标按均值/极差报告（默认 1）
  --judge                 额外用 CONGORAG_CHAT_* 让模型判一次回答是否被引用支撑
  --expect-corpus-hash H  语料指纹必须等于 H，否则退出
  --doc-timeout-ms N      等文档 ready 的上限，默认 600000（10 分钟）
  --question-timeout-ms N 单条提问的上限，默认 120000
  --help                  这一页

退出码
  0   跑完了（分数高低不影响退出码）
  64  参数或环境变量不对
  65  语料/数据集对不上、语料指纹不匹配、或跟基线不是同一份语料
  66  栈没起来（/healthz 不通），或服务端返回了意料之外的错误
  67  embedding 模型不匹配，或服务端还没有 embedding 模型
  68  文档处理 failed，或等 ready 超时
  70  内部错误
`;

// ────────────────────────────────────────────────────────────────
// 参数
// ────────────────────────────────────────────────────────────────

// parseArgs 是纯函数，方便 --help 这条冒烟路径只测它一件事。
export function parseArgs(argv) {
  const args = {
    help: false,
    apiBase: process.env.CONGORAG_API_BASE_URL ?? "http://127.0.0.1:3210",
    corpusDir: defaultCorpusDir(),
    datasetPath: defaultDatasetPath(),
    resultsDir: fileURLToPath(new URL("./results/", import.meta.url)),
    baselinePath: fileURLToPath(new URL("./baseline.json", import.meta.url)),
    writeBaseline: false,
    diffPaths: [],
    repeat: 1,
    judge: false,
    expectCorpusHash: null,
    docTimeoutMs: 600000,
    questionTimeoutMs: 120000,
  };

  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i];
    const next = () => {
      const v = argv[i + 1];
      if (v === undefined || v.startsWith("--")) {
        throw new UsageError(`${a} 后面缺少值`);
      }
      i += 1;
      return v;
    };
    switch (a) {
      case "--help":
      case "-h":
        args.help = true;
        break;
      case "--api-base":
        args.apiBase = next();
        break;
      case "--corpus":
        args.corpusDir = next();
        break;
      case "--dataset":
        args.datasetPath = next();
        break;
      case "--results-dir":
        args.resultsDir = next();
        break;
      case "--baseline":
        args.baselinePath = next();
        break;
      case "--write-baseline":
        args.writeBaseline = true;
        break;
      case "--repeat": {
        const n = Number(next());
        if (!Number.isInteger(n) || n < 1) throw new UsageError("--repeat 必须是正整数");
        args.repeat = n;
        break;
      }
      case "--judge":
        args.judge = true;
        break;
      case "--expect-corpus-hash":
        args.expectCorpusHash = next();
        break;
      case "--doc-timeout-ms":
        args.docTimeoutMs = Number(next());
        break;
      case "--question-timeout-ms":
        args.questionTimeoutMs = Number(next());
        break;
      case "--diff":
        // 吃掉后面连着的位置参数：一个 = 跟本次结果比，两个 = 两个文件互比。
        while (argv[i + 1] !== undefined && !argv[i + 1].startsWith("--")) {
          args.diffPaths.push(argv[i + 1]);
          i += 1;
        }
        if (args.diffPaths.length > 2) throw new UsageError("--diff 最多接两个路径");
        break;
      default:
        throw new UsageError(`不认识的参数: ${a}`);
    }
  }

  args.apiBase = args.apiBase.replace(/\/+$/, "");
  return args;
}

export class UsageError extends Error {}

// ────────────────────────────────────────────────────────────────
// HTTP 客户端：没有数据库、没有 docker，只有 fetch
// ────────────────────────────────────────────────────────────────

class ApiClient {
  constructor(baseUrl) {
    this.baseUrl = baseUrl;
  }

  url(p) {
    return `${this.baseUrl}${p}`;
  }

  async json(method, p, body) {
    const init = { method };
    if (body !== undefined) {
      init.headers = { "Content-Type": "application/json" };
      init.body = JSON.stringify(body);
    }
    const resp = await fetch(this.url(p), init);
    const text = await resp.text();
    let parsed = null;
    try {
      parsed = text === "" ? null : JSON.parse(text);
    } catch {
      parsed = null;
    }
    return { status: resp.status, ok: resp.ok, body: parsed, text };
  }
}

// ────────────────────────────────────────────────────────────────
// 栈的连通性、知识库、文档
// ────────────────────────────────────────────────────────────────

async function checkHealthz(client) {
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      const resp = await fetch(client.url("/healthz"));
      if (resp.ok) return true;
    } catch {
      // 连不上就是还没起，重试一次再说
    }
    await sleep(1000);
  }
  return false;
}

// recreateKnowledgeBase 先删掉所有叫 __eval__ 的知识库，再建一个干净的。
//
// 【为什么是"全部删掉"而不是"删掉找到的第一个"】knowledge_bases.name 上
// 没有唯一约束（migrations/0001_init.up.sql），历史运行失败、或者有人
// 手动建过，都可能留下好几个同名的。只删一个，剩下的会让上传的文档
// 分散在不同知识库里，而检索是按知识库过滤的。
async function recreateKnowledgeBase(client) {
  const listed = await client.json("GET", "/api/v1/knowledge-bases");
  if (!listed.ok) {
    throw new PreconditionError(`列知识库失败: HTTP ${listed.status} ${listed.text}`, EXIT.STACK);
  }
  const stale = (listed.body ?? []).filter((kb) => kb.name === EVAL_KB_NAME);
  for (const kb of stale) {
    const del = await client.json("DELETE", `/api/v1/knowledge-bases/${kb.id}`);
    if (!del.ok) {
      throw new PreconditionError(`删除旧知识库 ${kb.id} 失败: HTTP ${del.status}`, EXIT.STACK);
    }
  }

  const created = await client.json("POST", "/api/v1/knowledge-bases", { name: EVAL_KB_NAME });
  if (!created.ok) {
    throw new PreconditionError(`建知识库失败: HTTP ${created.status} ${created.text}`, EXIT.STACK);
  }
  return created.body.id;
}

async function uploadCorpus(client, kbId, corpus) {
  const uploaded = [];
  for (const file of corpus) {
    const form = new FormData();
    // 字段名必须是 file（apps/api/internal/api/server.go 的 FormFile("file")）。
    // 三参数 append 是为了带上原始文件名——服务端把它写进 documents.filename，
    // 也就是 citation 事件里的 filename。
    form.append("file", new Blob([file.text], { type: "text/markdown" }), file.name);

    const resp = await fetch(client.url(`/api/v1/knowledge-bases/${kbId}/documents`), {
      method: "POST",
      body: form,
    });
    if (!resp.ok) {
      throw new PreconditionError(`上传 ${file.name} 失败: HTTP ${resp.status} ${await resp.text()}`, EXIT.STACK);
    }
    const doc = await resp.json();
    uploaded.push({ filename: file.name, id: doc.id });
  }
  return uploaded;
}

// waitForDocuments 轮询到全部 ready。
//
// 【为什么 failed 是硬失败而不是低分】文档没进索引，跟这个问题语料里
// 本来就没有是两回事。把它当低分记下来，等于让"worker 挂了"看起来像
// "检索质量差"。
async function waitForDocuments(client, documents, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  const pending = new Map(documents.map((d) => [d.id, d.filename]));

  while (pending.size > 0) {
    for (const [id, filename] of [...pending]) {
      const resp = await client.json("GET", `/api/v1/documents/${id}`);
      if (!resp.ok) {
        throw new PreconditionError(`查文档 ${id} 状态失败: HTTP ${resp.status}`, EXIT.STACK);
      }
      const status = resp.body.status;
      if (status === "ready") {
        pending.delete(id);
      } else if (status === "failed") {
        throw new PreconditionError(
          `文档 ${filename}（${id}）处理失败——worker 的日志里有原因。` +
            "文档没进索引的话，这份报告里的检索数字全部不成立。",
          EXIT.DOCUMENT,
        );
      }
    }
    if (pending.size === 0) break;
    if (Date.now() > deadline) {
      throw new PreconditionError(
        `等文档 ready 超过 ${Math.round(timeoutMs / 1000)} 秒，还剩 ${pending.size} 份没处理完：` +
          [...pending.values()].join(", ") +
          "。检查 make dev-worker 是不是在跑、embedding 接口是不是通。",
        EXIT.DOCUMENT,
      );
    }
    await sleep(2000);
  }
}

// assertEmbeddingModel 确认当前生效的 embedding 模型和操作者的预期一致。
//
// 【为什么必须拦】换了 embedding 模型之后，旧向量和新查询不在同一个空间里，
// 检索结果基本等于随机。这种状态下跑出来的 recall 不是"低"，是"没有意义"，
// 所以它是前置条件失败，不是低分。
//
// 【"当前生效"是怎么算出来的】/api/v1/providers 不暴露"哪个是 active"，
// 但服务端的判据是 internal/llm/model.go 的 LatestByKind——同 kind 里
// createdAt 最新的那个。这里照着算一遍。这是复刻了服务端的实现细节，
// 一旦服务端改成显式的 active 标记，这里必须跟着改。
export function activeEmbeddingModel(providers) {
  const models = (providers ?? []).flatMap((p) => p.models ?? []);
  const embeddings = models
    .filter((m) => m.kind === "embedding")
    // 【为什么按 Date.parse 排而不是按字符串排】createdAt 是 RFC3339，
    // 带时区偏移（实测是 +08:00）。字符串比较只有在所有偏移都相同时
    // 才等价于时间比较——换一台机器/一个时区就会排出错序。
    .sort((a, b) => timestamp(b.createdAt) - timestamp(a.createdAt));
  return embeddings[0] ?? null;
}

function timestamp(v) {
  const t = Date.parse(v ?? "");
  return Number.isNaN(t) ? 0 : t;
}

// ────────────────────────────────────────────────────────────────
// 提问：发一条消息，收整条流
// ────────────────────────────────────────────────────────────────

async function askOne(client, conversationId, text, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  let reader = null;
  try {
    const resp = await fetch(client.url(`/api/v1/conversations/${conversationId}/messages`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
      signal: controller.signal,
    });

    if (!resp.ok || !resp.body) {
      // 这一支只在请求体校验失败时出现——一旦切到 SSE 模式，之后所有错误
      // 都走 error 事件，不会再改状态码（apps/api/internal/api/server.go）。
      const problem = await resp.json().catch(() => null);
      return {
        citations: [],
        answer: "",
        errored: true,
        errorType: problem?.type ?? `http_${resp.status}`,
      };
    }

    let state = newParserState();
    const frames = [];
    reader = resp.body.getReader();
    const decoder = new TextDecoder();

    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      const step = parseSSE(decoder.decode(value, { stream: true }), state);
      state = step.state;
      frames.push(...step.frames);
      // done 是正常终止；error 之后服务端也不会再写东西（Send 直接返回），
      // 不在这里停下的话就要一直挂到超时。
      if (frames.some((f) => f.event === "done" || f.event === "error")) break;
    }

    // 【不冲缓冲区里剩下的那半帧】帧是按空行结束的（docs/sse-protocol.md），
    // 没有空行结尾的残余不是一个完整事件。服务端每帧都写 \n\n，所以正常
    // 情况下缓冲区到这里就是空的；真留下残余，说明流在中途断了——
    // 那本来就该按"这次没收到"处理，而不是把半帧猜成一个事件。
    const err = errorOf(frames);
    return {
      citations: citationsOf(frames),
      answer: answerOf(frames),
      errored: err !== null,
      errorType: err ? err.type : null,
    };
  } catch (cause) {
    // 【一条题坏掉不该毁掉整轮】网络抖动、超时、连接被掐断都落到这里，
    // 记成这道题出错，继续问下一道。
    return {
      citations: [],
      answer: "",
      errored: true,
      errorType: cause?.name === "AbortError" ? "client_timeout" : "client_transport_error",
    };
  } finally {
    clearTimeout(timer);
    if (reader) {
      try {
        await reader.cancel();
      } catch {
        // 已经结束了，取消失败无所谓
      }
    }
  }
}

// ────────────────────────────────────────────────────────────────
// 可选的 LLM 裁判
// ────────────────────────────────────────────────────────────────

function judgeConfig() {
  const base = process.env.CONGORAG_CHAT_BASE_URL;
  const key = process.env.CONGORAG_CHAT_API_KEY;
  const model = process.env.CONGORAG_CHAT_MODEL;
  if (!base || !key || !model) return null;
  return { base: base.replace(/\/+$/, ""), key, model };
}

// judgeAnswer 用另一个模型判一次"这段回答有没有被给出的引用支撑"。
//
// 【为什么默认关着】它引入第二个模型、第二份不确定性、第二份账单，
// 而它要判的正是生成那一半已经随机的东西。开着它跑出来的数字，必须
// 和不开着的数字分开看。
async function judgeAnswer(cfg, question, run) {
  const evidence = run.citations.map((c) => c.snippet).join("\n---\n");
  const prompt =
    `问题：${question}\n\n` +
    `检索到的片段：\n${evidence || "（没有检索到任何片段）"}\n\n` +
    `模型的回答：\n${run.answer}\n\n` +
    "请只回答一个字：如果回答完全被上面的片段支撑，答「是」；" +
    "如果有任何片段之外的断言（包括编造事实、给出片段里没有的数字），答「否」。";
  try {
    const resp = await fetch(`${cfg.base}/chat/completions`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${cfg.key}` },
      body: JSON.stringify({
        model: cfg.model,
        messages: [{ role: "user", content: prompt }],
        max_tokens: 4,
      }),
    });
    if (!resp.ok) return { supported: null, error: `HTTP ${resp.status}` };
    const body = await resp.json();
    const text = body?.choices?.[0]?.message?.content ?? "";
    return { supported: text.includes("是"), error: null };
  } catch (cause) {
    return { supported: null, error: cause?.message ?? String(cause) };
  }
}

// ────────────────────────────────────────────────────────────────
// 打印
// ────────────────────────────────────────────────────────────────

function formatQuestionTable(records) {
  const lines = [];
  lines.push("id   | class          | 引用 | 命中排名 | 误拒 | facts | 通过 | 备注");
  lines.push("-----|----------------|------|----------|------|-------|------|------");
  for (const r of records) {
    const note = r.errored ? `出错: ${r.errorType}` : r.facts.missing.length > 0 ? `缺: ${r.facts.missing.join("|")}` : "";
    lines.push(
      [
        r.id.padEnd(4),
        String(r.class).padEnd(14),
        String(r.citationCount).padStart(4),
        String(r.hitRank ?? "-").padStart(8),
        (r.refused ? "是" : "否").padStart(4),
        (r.facts.evaluated ? (r.facts.hit ? "中" : "缺") : "—").padStart(5),
        (r.ok ? "是" : "否").padStart(4),
        note,
      ].join(" | "),
    );
  }
  return lines.join("\n");
}

function formatAggregate(summary, aggregated) {
  const lines = [];
  lines.push(`── 生成指标（${summary.counts.total} 题 × N 遍的极差）──`);
  for (const [key, m] of Object.entries(aggregated)) {
    if (m.block !== "generation") continue;
    lines.push(
      `  ${metricLabel(key).padEnd(26)} ${m.mean.toFixed(3)}  [${m.min.toFixed(3)}, ${m.max.toFixed(3)}]`,
    );
  }
  lines.push("");
  lines.push("── 检索指标在多遍之间是否逐位一致（不一致说明有非检索因素在影响检索侧）──");
  for (const [key, m] of Object.entries(aggregated)) {
    if (m.block !== "retrieval") continue;
    lines.push(`  ${metricLabel(key).padEnd(26)} ${m.stable ? "一致" : "不一致 ← 需要查"}`);
  }
  return lines.join("\n");
}

// ────────────────────────────────────────────────────────────────
// git
// ────────────────────────────────────────────────────────────────

function gitInfo() {
  try {
    const head = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf-8" }).trim();
    const status = execFileSync("git", ["status", "--porcelain"], { encoding: "utf-8" });
    return { head, clean: status.trim() === "" };
  } catch {
    // 不在 git 仓库里（或者没装 git）也能跑，只是报告里这一栏会是 null
    return { head: null, clean: null };
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// PreconditionError 自带退出码，这样主流程的 catch 不用再分辨一遍是
// 哪一类前置条件失败。
export class PreconditionError extends Error {
  constructor(message, code) {
    super(message);
    this.name = "PreconditionError";
    this.code = code ?? EXIT.STACK;
  }
}

// ────────────────────────────────────────────────────────────────
// 主流程
// ────────────────────────────────────────────────────────────────

export async function main(argv = process.argv.slice(2)) {
  let args;
  try {
    args = parseArgs(argv);
  } catch (cause) {
    if (cause instanceof UsageError) {
      console.error(cause.message);
      console.error("跑 node evals/run_eval.mjs --help 看用法。");
      return EXIT.USAGE;
    }
    throw cause;
  }

  if (args.help) {
    process.stdout.write(HELP);
    return EXIT.OK;
  }

  // ── 纯离线：两个已有报告互比，不需要栈 ──
  if (args.diffPaths.length === 2) {
    const [a, b] = args.diffPaths.map(readJsonFile);
    if (!a || !b) return EXIT.USAGE;
    console.log(formatDiff(compareReports(a, b), path.basename(args.diffPaths[0])));
    return EXIT.OK;
  }

  // ── 语料与数据集：先对一遍，不联网 ──
  const corpus = loadCorpus(args.corpusDir);
  const dataset = loadDataset(args.datasetPath);
  const problems = validateDataset(dataset, corpus);
  if (problems.length > 0) {
    console.error(`数据集校验失败，共 ${problems.length} 条（没有联网，直接退出）：`);
    for (const p of problems) console.error(`  - ${p}`);
    return EXIT.CORPUS;
  }

  const hash = corpusHash(corpus);
  const stats = datasetStats(dataset);
  const kValues = DEFAULT_K_VALUES;

  if (args.expectCorpusHash && args.expectCorpusHash !== hash) {
    console.error(
      `语料指纹不匹配：期望 ${args.expectCorpusHash}，实际 ${hash}。` +
        "语料变了，用旧指纹记录的结果不能和这次比。",
    );
    return EXIT.CORPUS;
  }

  // 【先查配置再联网】少一个环境变量这种事不该等到连上服务端之后才发现。
  const expectedEmbedModel = process.env.CONGORAG_EMBED_MODEL;
  if (!expectedEmbedModel) {
    console.error(
      "没有设置 CONGORAG_EMBED_MODEL。不设它就没法确认当前生效的 embedding 模型，" +
        "而模型一换，整张检索表都没有意义（见 --help）。",
    );
    return EXIT.USAGE;
  }

  // 基线：默认就是 evals/baseline.json，不存在的话只提示一句。
  const baselinePath = args.diffPaths[0] ?? args.baselinePath;
  let baseline = null;
  if (existsSync(baselinePath)) {
    baseline = readJsonFile(baselinePath);
    if (baseline?.corpusHash && baseline.corpusHash !== hash) {
      console.error(
        `基线 ${baselinePath} 是用另一份语料跑出来的（基线 ${baseline.corpusHash}，现在 ${hash}）。` +
          "两次的分数不可比，先 --write-baseline 换一份。",
      );
      return EXIT.CORPUS;
    }
  }

  console.log(`语料 ${corpus.length} 个文件，指纹 ${hash}`);
  console.log(
    `数据集 ${stats.total} 题（answerable ${stats.answerable} / should_refuse ${stats.should_refuse}）`,
  );
  console.log(`语料预计切成约 ${corpusChunkEstimate(corpus)} 个分块（K 取 ${kValues.join("/")}）`);

  // 锚点在语料文件上的分布：某道题挂掉的时候，第一件想知道的事是
  // "它问的那份文档这次传上去了吗"；另外，一个没有任何题目指向的语料
  // 文件只是在给检索添竞争项，看见了就该处理。
  const anchorMap = resolveAnchors(dataset, corpus);
  const perFile = new Map(corpus.map((f) => [f.name, 0]));
  for (const files of anchorMap.values()) {
    for (const name of files) perFile.set(name, (perFile.get(name) ?? 0) + 1);
  }
  console.log(
    "锚点分布 " +
      [...perFile.entries()].map(([name, n]) => `${name}:${n}`).join("  ") +
      "（数字是提问数）",
  );
  console.log("");

  // ── 栈 ──
  const client = new ApiClient(args.apiBase);
  if (!(await checkHealthz(client))) {
    console.error(`/healthz 不通（${args.apiBase}）。先 make up && make dev && make dev-worker。`);
    return EXIT.STACK;
  }

  try {
    // ── embedding 模型对不对 ──
    const providers = await client.json("GET", "/api/v1/providers");
    if (!providers.ok) {
      console.error(`取 /api/v1/providers 失败: HTTP ${providers.status}`);
      return EXIT.STACK;
    }
    const active = activeEmbeddingModel(providers.body);
    const expected = expectedEmbedModel;
    if (!active) {
      console.error("服务端还没有任何 embedding 模型，先去引导页配一个 provider。");
      return EXIT.MODEL;
    }
    if (active.modelId !== expected) {
      console.error(
        `embedding 模型不匹配：预期 ${expected}，当前生效的是 ${active.modelId}。` +
          "换模型之后旧向量和新查询不在同一个空间里，跑出来的数字没有意义。",
      );
      return EXIT.MODEL;
    }
    console.log(`embedding 模型 ${active.modelId}（维度 ${active.embeddingDim}）`);

    // ── 灌语料 ──
    const kbId = await recreateKnowledgeBase(client);
    console.log(`知识库 ${EVAL_KB_NAME} 重建完成 (${kbId})`);
    const documents = await uploadCorpus(client, kbId, corpus);
    console.log(`已上传 ${documents.length} 份文档，等 worker 处理…`);
    await waitForDocuments(client, documents, args.docTimeoutMs);
    console.log("全部文档 ready。");
    console.log("");

    // ── 逐遍提问 ──
    const judge = args.judge ? judgeConfig() : null;
    if (args.judge && !judge) {
      console.error(
        "--judge 需要 CONGORAG_CHAT_BASE_URL / CONGORAG_CHAT_API_KEY / CONGORAG_CHAT_MODEL 三个都设上。",
      );
      return EXIT.USAGE;
    }

    const passes = [];
    for (let pass = 1; pass <= args.repeat; pass += 1) {
      if (args.repeat > 1) console.log(`第 ${pass}/${args.repeat} 遍…`);

      // 【每一遍开一个新会话】同一个会话里，后面的问题会看到前面问过的
      // 内容和回答，多跑一遍就不再是同一组条件了。
      const conv = await client.json("POST", "/api/v1/conversations", {
        title: `${EVAL_CONV_TITLE}-${pass}`,
        knowledgeBaseId: kbId,
      });
      if (!conv.ok) {
        console.error(`建会话失败: HTTP ${conv.status} ${conv.text}`);
        return EXIT.STACK;
      }

      const records = [];
      for (const entry of dataset) {
        const q = entry.question;
        const run = await askOne(client, conv.body.id, q.question, args.questionTimeoutMs);
        if (judge && q.class === "answerable") {
          const verdict = await judgeAnswer(judge, q.question, run);
          run.judged = verdict;
        }
        records.push(scoreQuestion(q, run, { kValues }));
        process.stdout.write(`\r  已问 ${records.length}/${dataset.length}`);
      }
      process.stdout.write("\n");
      passes.push({ records, summary: summarise(records, { kValues }) });
    }

    const summary = passes[0].summary;
    const records = passes[0].records;
    const aggregates = args.repeat > 1 ? aggregatePasses(passes.map((p) => p.summary)) : null;

    const git = gitInfo();
    const report = buildReport({
      summary,
      records,
      corpusHash: hash,
      datasetHash: sha256Hex(readFileSync(args.datasetPath, "utf-8").replace(/\r\n/g, "\n")),
      datasetLines: dataset.map((e) => ({ id: e.question.id, hash: e.hash })),
      gitHead: git.head,
      gitClean: git.clean,
      apiBaseUrl: args.apiBase,
      embeddingModel: active.modelId,
      chatModel: judge?.model ?? null,
      repeats: args.repeat,
      passSummaries: passes.map((p) => p.summary),
    });

    // ── 报告 ──
    //
    // 【为什么退步对比打在最前面】默认就跟 evals/baseline.json 比，而且
    // 打在第一屏——跑一次 eval 的代价是几分钟和真金白银，如果第一眼看
    // 不到"哪一项比上次差"，剩下那些数字就没什么人会看。
    console.log("");
    if (!args.writeBaseline) {
      console.log(formatDiff(compareReports(baseline, report), path.basename(baselinePath)));
      console.log("");
    }
    console.log(formatReport(summary, {}));
    console.log("");
    console.log(formatQuestionTable(records));
    if (aggregates) {
      console.log("");
      console.log(formatAggregate(summary, aggregates));
    }

    if (args.judge) {
      const judged = records.filter((r) => r.judged && r.judged.supported !== null);
      const ok = judged.filter((r) => r.judged.supported).length;
      console.log("");
      console.log("── 裁判指标（另一次模型调用，不属于上面两个 block）──");
      console.log(
        `  回答被引用支撑的比例        ${judged.length === 0 ? "—" : (ok / judged.length).toFixed(3)}  ${ok}/${judged.length}`,
      );
    }

    // ── 落盘 ──
    mkdirSync(args.resultsDir, { recursive: true });
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    const short = (git.head ?? "nogit").slice(0, 12);
    const outPath = path.join(args.resultsDir, `${stamp}-${short}.json`);
    writeFileSync(outPath, JSON.stringify(report, null, 2) + "\n");
    console.log("");
    console.log(`结果已写入 ${outPath}`);

    if (args.writeBaseline) {
      const baselineBody = {
        ...report,
        note: "这是 evals/run_eval.mjs --write-baseline 写出来的基线。",
      };
      writeFileSync(args.baselinePath, JSON.stringify(baselineBody, null, 2) + "\n");
      console.log(`基线已写入 ${args.baselinePath}`);
    }

    return EXIT.OK;
  } catch (cause) {
    if (cause instanceof PreconditionError) {
      console.error(cause.message);
      // 具体是哪一类前置条件，由抛出的地方决定（见 EXIT 那张表）。
      return cause.code ?? EXIT.STACK;
    }
    console.error(cause instanceof Error ? cause.stack : String(cause));
    return EXIT.INTERNAL;
  }
}

function readJsonFile(p) {
  try {
    return JSON.parse(readFileSync(p, "utf-8"));
  } catch (cause) {
    console.error(`读不了 ${p}: ${cause.message}`);
    return null;
  }
}

// 只有直接 node evals/run_eval.mjs 时才跑主流程；被 import 时不产生副作用。
const invokedDirectly =
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;

if (invokedDirectly) {
  main().then(
    (code) => process.exit(code),
    (cause) => {
      console.error(cause instanceof Error ? cause.stack : String(cause));
      process.exit(EXIT.INTERNAL);
    },
  );
}
