// score.mjs —— eval 的"打分"层：SSE 帧解析 + 全部指标计算。
//
// 这个文件里没有一行 IO：输入是已经收到的帧和文本，输出是数字。这样做的
// 直接好处是 CI 只需要跑这一层的单测（用手写的 .sse fixture 当输入），
// 不需要起栈、不需要真的调模型——见 evals/README.md「为什么全量 eval 不进 CI」。
//
// 【指标为什么分两块】契约里 SendMessageRequest 只有 text 一个字段
// （contracts/openapi.yaml），没有 temperature，也没有 seed。检索那一半
// 是确定性的（同样的语料 + 同样的 embedding 模型 → 同样的最近邻），
// 生成那一半不是。把两者混在一张表里，会让人以为整张表都只差一点噪声，
// 实际上生成指标每次跑都可能不一样。所以报告里强制分成两块打印。

import { createHash } from "node:crypto";

// 衡量检索用的 K。上界是 5，不是 10——internal/retrieval/usecase.go 的
// defaultTopK = 5，而 internal/conversation/usecase.go 调 Search 时没传
// TopK（零值），所以产品面上到手的引用最多 5 条。K=10 从这个界面根本
// 够不着，写进报告只会是一列恒等于 recall@5 的数字。
export const DEFAULT_K_VALUES = [1, 3, 5];

// 默认的"拒答判据"。
//
// 【为什么是子串而不是让模型打分】拒答检测本来就只能是启发式的：与其
// 引入一次额外的模型调用（又多一份不确定性），不如用一个明确的词表，
// 让判据可复现、可审计。代价是它会漏掉措辞不同的拒答——这一条在
// README 的"已知不足"里写明了，不要把拒答率当成精确值。
export const DEFAULT_REFUSAL_MARKERS = [
  "没有",
  "未提及",
  "未提供",
  "无法",
  "不清楚",
  "不知道",
  "不涉及",
  "不包含",
  "抱歉",
];

// ────────────────────────────────────────────────────────────────
// SSE 帧解析
// ────────────────────────────────────────────────────────────────

// 状态就是一个缓冲区 + 一个续传游标。做成显式状态、纯函数返回新状态，
// 是为了让"一帧跨两次 read"这件事在单测里可以被精确构造出来。
export function newParserState() {
  return { buffer: "", lastEventId: null };
}

// frameSeparators 是三种合法的帧分隔符。
//
// 【SSE 规范允许 CR、LF、CRLF 三种行结束符】只按 "\n\n" 切会在两种情况下
// 静默解析出 0 帧：一是服务端用 CRLF（`\r\n\r\n` 里两个 \n 之间夹着 \r，
// `indexOf("\n\n")` 找不到）；二是把 fixture 检出到 Windows 上——git 的
// autocrlf 会把它们转成 CRLF。后者真的发生过：同一份测试在 Linux CI 上全绿、
// 在 Windows 本地全红（14 条）。
//
// 三种都认之后，解析结果与行结束符无关——这正是这一步该有的性质。
const frameSeparators = ["\r\n\r\n", "\n\n", "\r\r"];

// splitFrames 找出最早的帧边界，返回 [完整帧数组, 剩余缓冲]。
function splitFrames(buffer) {
  const frames = [];
  let rest = buffer;

  for (;;) {
    let at = -1;
    let sepLen = 0;
    for (const sep of frameSeparators) {
      const i = rest.indexOf(sep);
      if (i !== -1 && (at === -1 || i < at)) {
        at = i;
        sepLen = sep.length;
      }
    }
    if (at === -1) break;

    frames.push(rest.slice(0, at));
    rest = rest.slice(at + sepLen);
  }
  return { frames, rest };
}

// parseSSE 吃一块字节（已经解码成字符串），吐出一批完整的帧。
//
// 【为什么要留缓冲区】TCP 不保证一次 read 刚好落在一帧的边界上
// （docs/sse-protocol.md「帧格式」一节点名的坑）。按帧分隔符切，
// 最后一段永远是不完整的（或空串），留在状态里等下一次。
export function parseSSE(chunk, state = newParserState()) {
  const buffer = state.buffer + chunk;
  const frames = [];
  let lastEventId = state.lastEventId;

  // 【缓冲区里也要容忍 CRLF】上面的分隔符列表已经认了 \r\n\r\n，但单独一个
  // 行结束符（帧内）也要认——parseFrame 按 /\r\n|\r|\n/ 切行。
  const { frames: parts, rest } = splitFrames(buffer);

  for (const raw of parts) {
    const frame = parseFrame(raw);
    if (frame === null) continue; // 心跳注释行
    // 【游标只在真的带了 id 时才推进】那条"事件没能落库"的兜底 error 帧
    // 是没有 id 的（apps/api/internal/api/sse.go）。拿它当游标会让续传
    // 从这里往后跳过一批还没收到的事件。
    if (frame.id !== null) {
      const n = Number(frame.id);
      if (!Number.isNaN(n)) lastEventId = n;
    }
    frames.push(frame);
  }

  return { frames, state: { buffer: rest, lastEventId } };
}

// parseSSEString 是把一整段文本一次喂进去的便利函数，只给测试和离线分析用。
export function parseSSEString(text) {
  return parseSSE(text, newParserState()).frames;
}

function parseFrame(raw) {
  let id = null;
  let type = "";
  const dataLines = [];

  // 【按三种行结束符切行】SSE 规范里 CR、LF、CRLF 都合法，而且 fixture 在不同
  // 平台上被 git 检出成什么样子取决于 autocrlf——只认 \n 的话每一行尾部都会
  // 留一个 \r，`id: 42\r` 解析出来的 id 是 "42\r"、Number() 变 NaN。
  for (const line of raw.split(/\r\n|\r|\n/)) {
    if (line === "") continue;
    if (line.startsWith(":")) continue; // 心跳：以 ":" 开头的是注释
    const sep = line.indexOf(":");
    if (sep === -1) continue; // 不合规范的行，忽略
    const field = line.slice(0, sep);
    let value = line.slice(sep + 1);
    if (value.startsWith(" ")) value = value.slice(1);

    if (field === "id") id = value;
    else if (field === "event") type = value;
    else if (field === "data") dataLines.push(value);
  }

  // 既没有 event 也没有 data 的段 = 心跳，不产生事件。
  if (type === "" && dataLines.length === 0) return null;
  if (type === "") type = "message";

  const data = dataLines.join("\n");
  let json = null;
  try {
    json = JSON.parse(data);
  } catch {
    json = null; // 坏帧不打断整条流（和 web/src/lib/streamChat.ts 的取舍一致）
  }

  return { id, event: type, data, json };
}

// payloadOf 取出真正有用的那一层。
//
// 【为什么要往下挖一层】conversation.emitEvent 把 payload 整个包进
// {type, data} 再序列化（internal/conversation/usecase.go 的 emitEvent），
// 所以线路上的 data 行长这样：
//   {"type":"citation","data":{"chunkId":...,"snippet":...}}
// 也就是说 event 名和 payload.type 是同一个值，真正的内容在 .data 里。
// 兜底 error 帧用的是同一个包装（sse.go 的 writeFallbackError），
// 所以这里一个判据就够，不需要为它开分支。
export function payloadOf(frame) {
  const body = frame?.json?.data;
  return body !== null && typeof body === "object" ? body : {};
}

export function citationsOf(frames) {
  return frames
    .filter((f) => f.event === "citation")
    .map((f) => {
      const p = payloadOf(f);
      return {
        chunkId: typeof p.chunkId === "string" ? p.chunkId : "",
        documentId: typeof p.documentId === "string" ? p.documentId : "",
        filename: typeof p.filename === "string" ? p.filename : "",
        // 服务端填的是分块的完整正文（internal/ctxmgr/usecase.go 的
        // Snippet: c.Content），所以锚点比对是在整段文字里找子串，
        // 不需要 psql、不需要 docker exec。
        snippet: typeof p.snippet === "string" ? p.snippet : "",
        score: typeof p.score === "number" ? p.score : null,
      };
    });
}

export function answerOf(frames) {
  return frames
    .filter((f) => f.event === "token")
    .map((f) => {
      const p = payloadOf(f);
      return typeof p.text === "string" ? p.text : "";
    })
    .join("");
}

export function errorOf(frames) {
  const frame = frames.find((f) => f.event === "error");
  if (!frame) return null;
  const p = payloadOf(frame);
  return {
    id: frame.id,
    type: typeof p.type === "string" ? p.type : null,
    detail: typeof p.detail === "string" ? p.detail : null,
  };
}

// ────────────────────────────────────────────────────────────────
// 单题打分
// ────────────────────────────────────────────────────────────────

// matchAnchor 返回"第一条命中锚点的引用"的序号（1 开始），没命中返回 null。
//
// 【为什么取第一条就够】引用是按相关度从高到低到达的
// （internal/ctxmgr/model.go 先排好序，usecase 取前缀），所以第一条命中
// 的位置就是 MRR 需要的那个排名；同一条锚点后面再出现不改变排名。
export function matchAnchor(citations, anchors) {
  if (!Array.isArray(anchors) || anchors.length === 0) return null;
  for (let i = 0; i < citations.length; i += 1) {
    const snippet = citations[i].snippet;
    if (snippet && anchors.some((a) => snippet.includes(a))) return i + 1;
  }
  return null;
}

export function isRefusal(text, markers = DEFAULT_REFUSAL_MARKERS) {
  if (typeof text !== "string" || text.trim() === "") return false;
  return markers.some((m) => text.includes(m));
}

export function evaluateFacts(text, facts) {
  const list = Array.isArray(facts) ? facts : [];
  if (list.length === 0) return { hit: true, missing: [], evaluated: false };
  const haystack = (text ?? "").toLowerCase();
  const missing = list.filter((f) => !haystack.includes(String(f).toLowerCase()));
  return { hit: missing.length === 0, missing, evaluated: true };
}

// scoreQuestion 把一个问题的"运行结果"折成一条可比较的记录。
//
// run = { citations, answer, errored, errorType }
//
// 【出错的问题算 0 分，不算缺席】把它从分母里拿掉，等于让"跑崩了"变成
// 一种提高分数的手段。这里把它当成没答对，另外单独统计条数。
export function scoreQuestion(question, run, opts = {}) {
  const kValues = opts.kValues ?? DEFAULT_K_VALUES;
  const markers = question.refusalMarkers ?? DEFAULT_REFUSAL_MARKERS;
  const citations = run.citations ?? [];
  const answer = run.answer ?? "";

  const rank = question.class === "answerable" ? matchAnchor(citations, question.anchors) : null;
  const refused = isRefusal(answer, markers);
  const facts = evaluateFacts(answer, question.facts);

  const recallAt = {};
  for (const k of kValues) recallAt[k] = rank !== null && rank <= k ? 1 : 0;

  const record = {
    id: question.id,
    class: question.class,
    errored: Boolean(run.errored),
    errorType: run.errorType ?? null,
    citationCount: citations.length,
    hitRank: rank,
    recallAt,
    refused,
    facts,
    // 只用于"观测到的不同引用片段数"——它是判断语料规模够不够的现场证据，
    // 不参与任何好坏判断，所以不进结果文件的 per-question 部分。
    snippets: citations.map((c) => c.snippet),
    ok: false,
  };
  record.ok = questionPassed(record);
  return record;
}

// questionPassed 是"这道题这次跑过去了吗"的单题判据，用来算 --diff 里的
// "哪些题从通过翻成了不通过"。
//
// answerable：没出错、没被误拒、facts 全中、而且锚点确实被某条引用覆盖了。
// should_refuse：没出错、而且拒答了。
//
// 【为什么 answerable 用"锚点被任何一条引用覆盖"而不是 recall@5】recall@K
// 是给指标的，K 是个外部选择；单题通过与否不该随 K 变化。分块本来就少，
// K=5 时两者几乎等价，等语料长大之后它们才会分开。
export function questionPassed(record) {
  if (record.errored) return false;
  if (record.class === "should_refuse") return record.refused;
  return !record.refused && record.facts.hit && record.hitRank !== null;
}

// ────────────────────────────────────────────────────────────────
// 汇总
// ────────────────────────────────────────────────────────────────

export function median(values) {
  const xs = [...values].sort((a, b) => a - b);
  if (xs.length === 0) return null;
  const mid = Math.floor(xs.length / 2);
  return xs.length % 2 === 1 ? xs[mid] : (xs[mid - 1] + xs[mid]) / 2;
}

function ratio(numerator, denominator) {
  return denominator === 0 ? 0 : numerator / denominator;
}

function metric(block, value, direction, numerator, denominator) {
  return {
    block,
    value,
    direction,
    numerator: numerator ?? null,
    denominator: denominator ?? null,
  };
}

// summarise 把一批单题记录折成指标表。
//
// 分成两个 block：
//   retrieval —— 确定性的，只跟语料、embedding 模型、检索参数有关
//   generation —— 随机的，跟模型的采样有关
// 外加 block: "info" 的几条上下文数字，它们不参与好坏判断。
export function summarise(records, opts = {}) {
  const kValues = opts.kValues ?? DEFAULT_K_VALUES;
  const maxK = Math.max(...kValues);

  const answerable = records.filter((r) => r.class === "answerable");
  const refusable = records.filter((r) => r.class === "should_refuse");

  const metrics = {};

  // ── 检索：context recall@K ──
  //
  // 【名字里为什么要带 context】它测的不是"检索器找回了多少真正相关的
  // 分块"，而是"最终进入上下文的引用里，有多少覆盖了人工写下的锚点"。
  // 引用列表在预算那一步被从头截断过（internal/ctxmgr/usecase.go），
  // 所以这是真实检索召回率的**下界**，不是等于。名字里带 context 是为了
  // 不让人把它当成检索器的召回率去跟 measure_recall.sh 的数字对比。
  for (const k of kValues) {
    const numerator = answerable.filter((r) => r.recallAt[k] === 1).length;
    metrics[`context_recall@${k}`] = metric(
      "retrieval",
      ratio(numerator, answerable.length),
      "higher",
      numerator,
      answerable.length,
    );
  }

  // ── 检索：MRR ──
  const mrrSum = answerable.reduce(
    (acc, r) => acc + (r.hitRank === null ? 0 : 1 / r.hitRank),
    0,
  );
  metrics["mrr"] = metric("retrieval", ratio(mrrSum, answerable.length), "higher");

  // ── 信息：引用条数中位数、观测到的不同片段数 ──
  metrics["median_citations"] = metric(
    "info",
    median(records.map((r) => r.citationCount)) ?? 0,
    "neutral",
  );

  const snippets = new Set();
  for (const r of records) for (const s of r.snippets ?? []) snippets.add(s);
  metrics["observed_unique_snippets"] = metric("info", snippets.size, "neutral");

  // 【这条阈值判断是这份报告里最重要的免责声明】语料只有几 KB 时，
  // 分块总数可能不超过 5，而产品面的 top-K 上界正好也是 5——任何一次
  // 检索都会把全部或近乎全部分块拿回来，recall@5 于是退化成"恒等于 1"。
  // 这个数一旦不大于 maxK，报告就会自动打一行警告。
  metrics["corpus_covers_topk"] = metric("info", snippets.size > maxK ? 1 : 0, "neutral");

  // ── 生成：拒答准确率、被误拒率、facts 命中率 ──
  const refusedRefusable = refusable.filter((r) => r.refused).length;
  metrics["refusal_accuracy"] = metric(
    "generation",
    ratio(refusedRefusable, refusable.length),
    "higher",
    refusedRefusable,
    refusable.length,
  );

  const wronglyRefused = answerable.filter((r) => r.refused).length;
  metrics["wrongly_refused_rate"] = metric(
    "generation",
    ratio(wronglyRefused, answerable.length),
    "lower",
    wronglyRefused,
    answerable.length,
  );

  const withFacts = answerable.filter((r) => r.facts.evaluated);
  const factsHit = withFacts.filter((r) => r.facts.hit).length;
  metrics["facts_hit_rate"] = metric(
    "generation",
    ratio(factsHit, withFacts.length),
    "higher",
    factsHit,
    withFacts.length,
  );

  return {
    counts: {
      total: records.length,
      answerable: answerable.length,
      should_refuse: refusable.length,
      errored: records.filter((r) => r.errored).length,
      passed: records.filter((r) => r.ok).length,
    },
    metrics,
    kValues,
    maxK,
  };
}

// aggregatePasses 把 --repeat N 跑出来的 N 份汇总折成 均值 [最小, 最大]。
// 只对 generation 那几项有意义——retrieval 在 N 遍之间应当逐位相同。
export function aggregatePasses(summaries) {
  const keys = new Set();
  for (const s of summaries) for (const k of Object.keys(s.metrics)) keys.add(k);

  const out = {};
  for (const key of keys) {
    const present = summaries.map((s) => s.metrics[key]).filter(Boolean);
    if (present.length === 0) continue;
    const values = present.map((m) => m.value);
    out[key] = {
      block: present[0].block,
      direction: present[0].direction,
      mean: values.reduce((a, b) => a + b, 0) / values.length,
      min: Math.min(...values),
      max: Math.max(...values),
      passes: values.length,
      // 检索类的指标如果 N 遍之间不一致，那本身就是个需要看的信号
      stable: values.every((v) => v === values[0]),
    };
  }
  return out;
}

// ────────────────────────────────────────────────────────────────
// 报告
// ────────────────────────────────────────────────────────────────

export const METRIC_LABELS = {
  "context_recall@1": "context recall@1",
  "context_recall@3": "context recall@3",
  "context_recall@5": "context recall@5",
  mrr: "MRR",
  median_citations: "引用条数中位数",
  observed_unique_snippets: "观测到的不同引用片段数",
  corpus_covers_topk: "语料规模是否超过 top-K",
  refusal_accuracy: "should_refuse 正确拒答率",
  wrongly_refused_rate: "answerable 被误拒率",
  facts_hit_rate: "facts 命中率",
};

export function metricLabel(key) {
  return METRIC_LABELS[key] ?? key;
}

function fmtValue(key, value) {
  if (key === "median_citations") return value.toFixed(1);
  if (key === "observed_unique_snippets") return String(value);
  if (key === "corpus_covers_topk") return value ? "是" : "否";
  return value.toFixed(3);
}

function fmtRatio(m) {
  if (m.numerator === null || m.denominator === null) return "";
  return `${m.numerator}/${m.denominator}`;
}

// formatReport 只负责排版，不碰数字。两个 block 分别打印，
// 块标题里把"确定性 / 随机"写在明面上。
export function formatReport(summary, opts = {}) {
  const lines = [];
  const { counts, metrics, maxK } = summary;

  lines.push(
    `题目 ${counts.total}（answerable ${counts.answerable} / should_refuse ${counts.should_refuse}），单题通过 ${counts.passed}` +
      (counts.errored > 0 ? `，出错 ${counts.errored}（出错按未答对计分）` : ""),
  );
  lines.push("");

  lines.push("── 检索指标（确定性：同样的语料和 embedding 模型就该得到同样的数）──");
  for (const key of Object.keys(metrics)) {
    const m = metrics[key];
    if (m.block !== "retrieval") continue;
    lines.push(`  ${metricLabel(key).padEnd(26)} ${fmtValue(key, m.value).padStart(7)}  ${fmtRatio(m)}`);
  }
  if (metrics.corpus_covers_topk && metrics.corpus_covers_topk.value === 0) {
    lines.push(
      `  ⚠ 观测到的不同引用片段只有 ${metrics.observed_unique_snippets.value} 个，不超过 K=${maxK}——` +
        "语料太小时引用列表天然覆盖全语料，recall@K 会退化成恒等于 1，这几行数字只能当链路是否打通看，不能当索引质量看。",
    );
  }
  lines.push("");

  lines.push("── 生成指标（随机：契约里没有 temperature，同样的输入不保证同样的输出）──");
  lines.push(`  ${"（建议加 --repeat 3 看极差，单次数字不要当结论）"}`);
  for (const key of Object.keys(metrics)) {
    const m = metrics[key];
    if (m.block !== "generation") continue;
    lines.push(`  ${metricLabel(key).padEnd(26)} ${fmtValue(key, m.value).padStart(7)}  ${fmtRatio(m)}`);
  }
  lines.push("");

  if (opts.diff) {
    lines.push(formatDiff(opts.diff, opts.baselineName ?? "baseline"));
  }

  return lines.join("\n");
}

// formatDiff 返回一个字符串（不是行数组）——调用方要么把它拼进大报告，
// 要么直接打印，返回数组两边都会踩坑。
export function formatDiff(diff, baselineName = "baseline") {
  const lines = [];
  if (!diff.hasPrev) {
    lines.push(`── 与 ${baselineName} 的对比 ──`);
    lines.push(`  ${diff.reason}`);
    return lines.join("\n");
  }
  lines.push(`── 与 ${baselineName} 的对比（正数 = 本次更高）──`);
  const worse = diff.deltas.filter((d) => d.worse);
  if (diff.deltas.length === 0) {
    lines.push("  没有可对比的同名指标。");
  }
  for (const d of diff.deltas) {
    const arrow = d.delta === 0 ? "=" : d.delta > 0 ? "+" : "-";
    const mark = d.worse ? "  ← 变差" : "";
    lines.push(
      `  ${metricLabel(d.key).padEnd(26)} ${fmtDelta(d.delta).padStart(8)}  (${fmtNum(d.prev)} → ${fmtNum(d.cur)})${mark}`,
    );
  }
  lines.push("");
  if (diff.flipped.pass_to_fail.length > 0) {
    lines.push(`  从通过翻成不通过（${diff.flipped.pass_to_fail.length}）：${diff.flipped.pass_to_fail.join(", ")}`);
  } else {
    lines.push("  没有题目从通过翻成不通过。");
  }
  if (diff.flipped.fail_to_pass.length > 0) {
    lines.push(`  从不通过翻成通过（${diff.flipped.fail_to_pass.length}）：${diff.flipped.fail_to_pass.join(", ")}`);
  }
  if (diff.flipped.only_prev.length > 0) {
    lines.push(`  只在 ${baselineName} 里有的题（数据集被删了？）：${diff.flipped.only_prev.join(", ")}`);
  }
  if (diff.flipped.only_cur.length > 0) {
    lines.push(`  只在本次有的题（数据集被加了？）：${diff.flipped.only_cur.join(", ")}`);
  }
  if (worse.length > 0) {
    lines.push("");
    lines.push(`  退步指标 ${worse.length} 项：${worse.map((d) => metricLabel(d.key)).join(", ")}`);
  }
  return lines.join("\n");
}

function fmtNum(v) {
  return typeof v === "number" ? v.toFixed(3) : String(v);
}

function fmtDelta(v) {
  return (v >= 0 ? "+" : "") + v.toFixed(3);
}

// compareReports 是 --diff 的全部逻辑，纯函数。
export function compareReports(prev, cur, opts = {}) {
  const result = {
    hasPrev: false,
    reason: "",
    deltas: [],
    flipped: { pass_to_fail: [], fail_to_pass: [], only_prev: [], only_cur: [] },
  };

  if (!prev) {
    result.reason = "没有找到基线文件。先跑一次 --write-baseline。";
    return result;
  }
  if (prev.status === "not-yet-produced" || !prev.metrics) {
    result.reason =
      "基线文件存在但标记为“尚未产生真实数据”（status: not-yet-produced）——它不是一次真实运行的产物，" +
      "所以不做对比。跑一次 node evals/run_eval.mjs --write-baseline 之后再来看。";
    return result;
  }

  result.hasPrev = true;

  for (const key of Object.keys(cur.metrics)) {
    const p = prev.metrics[key];
    const c = cur.metrics[key];
    if (!p) continue;
    const delta = c.value - p.value;
    const worse =
      c.direction === "higher" ? delta < 0 : c.direction === "lower" ? delta > 0 : false;
    result.deltas.push({ key, prev: p.value, cur: c.value, delta, direction: c.direction, worse });
  }

  const prevById = new Map((prev.questions ?? []).map((q) => [q.id, q]));
  const curById = new Map((cur.questions ?? []).map((q) => [q.id, q]));
  for (const [id, q] of curById) {
    const p = prevById.get(id);
    if (!p) {
      result.flipped.only_cur.push(id);
      continue;
    }
    if (p.ok && !q.ok) result.flipped.pass_to_fail.push(id);
    else if (!p.ok && q.ok) result.flipped.fail_to_pass.push(id);
  }
  for (const id of prevById.keys()) {
    if (!curById.has(id)) result.flipped.only_prev.push(id);
  }

  result.flipped.pass_to_fail.sort();
  result.flipped.fail_to_pass.sort();
  result.flipped.only_prev.sort();
  result.flipped.only_cur.sort();
  return result;
}

// ────────────────────────────────────────────────────────────────
// 结果文件
// ────────────────────────────────────────────────────────────────

export function sha256Hex(input) {
  return createHash("sha256").update(input).digest("hex");
}

// buildReport 组装要写进 evals/results/ 的那份 JSON。
//
// 【为什么连 git 状态一起记】一个分数只有在"哪份代码 + 哪份语料 + 哪份
// 数据集"都确定的时候才有意义。语料和数据集用指纹，代码用 HEAD 加上
// "工作区当时是不是干净的"——不干净的话，HEAD 就不是这次跑的真实代码。
export function buildReport({
  summary,
  records,
  corpusHash,
  datasetHash,
  datasetLines,
  gitHead,
  gitClean,
  apiBaseUrl,
  embeddingModel,
  chatModel = null,
  repeats = 1,
  passSummaries = null,
}) {
  return {
    generatedAt: new Date().toISOString(),
    corpusHash,
    datasetHash,
    datasetLines,
    gitHead,
    gitClean,
    apiBaseUrl,
    embeddingModel,
    chatModel,
    repeats,
    counts: summary.counts,
    kValues: summary.kValues,
    metrics: summary.metrics,
    aggregated: passSummaries ? aggregatePasses(passSummaries) : null,
    questions: records.map((r) => ({
      id: r.id,
      class: r.class,
      errored: r.errored,
      errorType: r.errorType,
      citationCount: r.citationCount,
      hitRank: r.hitRank,
      refused: r.refused,
      factsHit: r.facts.hit,
      factsMissing: r.facts.missing,
      ok: r.ok,
    })),
  };
}
