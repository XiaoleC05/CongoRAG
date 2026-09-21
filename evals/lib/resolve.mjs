// resolve.mjs —— eval 的"真相校对"层：把数据集里的人写锚点，落到语料的具体文件上。
//
// 【为什么锚点不是 chunk_id】分块的 id 是每行 uuid.New() 现生成的
// （internal/retrieval/postgres.go），而重建索引走的是"先删后插"
// （internal/retrieval/usecase.go），所以每次重新处理、每次重新上传、
// 每次任务重试，同一段内容都会换一个新的 id。把它写进数据集，等于把
// 数据集绑死在一次特定的索引状态上——换一次 embedding 模型就全红。
// 跨运行稳定的只有分块的内容，所以锚点是内容片段。
//
// 【为什么不在这里连数据库】锚点要拿来比对的东西是 citation 事件里的
// snippet，而 snippet 填的就是分块的完整正文
// （internal/ctxmgr/usecase.go 的 Snippet: c.Content）。整个比对在
// 内存里就能做完，不需要 psql、不需要 docker exec，也就不需要把
// 连接串交给一个测量脚本。measure_recall.sh 走的是另一条路（它要
// 直接查表算 ANN 召回率），两者不是一回事。
//
// 纯函数 + 一个 CLI 入口。CLI 用来在跑 eval 之前先把数据集检查一遍，
// 也能单独跑：node evals/lib/resolve.mjs

import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import path from "node:path";

// MAX_ANCHOR_RUNES 和 internal/knowledge/pipeline.go 的 maxChunkChars 是
// 同一个数，必须一致。这个上界保证"一个锚点必定完整地落在某一个分块里"——
// 切分是贪心装段落，单个段落只要不超过 maxChunkChars 就不会被硬切。
// 一旦 pipeline.go 改了这个常量（或者换成分标题、按语义切分的策略），
// 这条保证就没了，数据集必须跟着重新核对。
export const MAX_ANCHOR_RUNES = 1200;

// PIPELINE_HINT 只在报错信息里出现，指路用的。
const PIPELINE_HINT =
  "（切分策略可能变了，见 internal/knowledge/pipeline.go 的 maxChunkChars）";

export class CorpusError extends Error {}
export class DatasetError extends Error {}

// normalizeNewlines 把 CRLF / CR 统一成 LF。
//
// 【为什么这里也要做一遍】服务端 parseAndChunk 第一步就是 normalizeNewlines，
// 所以分块正文里的换行永远是 LF。如果语料文件在磁盘上是 CRLF（Windows 上
// core.autocrlf=true 的默认后果），不归一化的话：锚点比对会失败（文件里是
// \r\n、snippet 里是 \n），指纹也会因为一次 checkout 而变。归一化之后，
// 磁盘换行风格不影响任何一个结论——指纹只认内容。
export function normalizeNewlines(s) {
  return s.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
}

// runeLength 数的是 Unicode 码点，不是 UTF-16 码元、也不是字节。
// 服务端的 maxChunkChars 用的是 utf8.RuneCountInString，同一个口径。
export function runeLength(s) {
  return [...s].length;
}

function sha256Hex(input) {
  return createHash("sha256").update(input).digest("hex");
}

// loadCorpus 读一个目录下所有的 .md，返回 [{ name, text }]。
// name 是文件名（不含目录），text 是归一化过换行的内容。
export function loadCorpus(dir) {
  let entries;
  try {
    entries = readdirSync(dir);
  } catch (err) {
    throw new CorpusError(`读不到语料目录 ${dir}: ${err.message}`);
  }
  const files = entries.filter((f) => f.endsWith(".md")).sort();
  if (files.length === 0) {
    throw new CorpusError(`语料目录 ${dir} 里一个 .md 都没有`);
  }
  return files.map((name) => ({
    name,
    text: normalizeNewlines(readFileSync(path.join(dir, name), "utf-8")),
  }));
}

// loadCorpusFromDisk 是 CLI 用的相对定位：resolve.mjs 在 evals/lib/ 下，
// 语料在 evals/corpus/。用 import.meta.url 而不是 process.cwd()，
// 这样从仓库任何位置调用都是同一份语料。
export function defaultCorpusDir() {
  return fileURLToPath(new URL("../corpus/", import.meta.url));
}

export function defaultDatasetPath() {
  return fileURLToPath(new URL("../dataset.jsonl", import.meta.url));
}

// normalizeCorpus 把一份语料里的换行统一掉。
//
// 【为什么指纹和锚点比对都先过这一步】换行风格是磁盘/checkout 的属性，
// 不是内容的属性：git 的 core.autocrlf 能让同一份文件在不同机器上一个是
// CRLF 一个是 LF。如果不过这一步，"指纹变了"和"锚点找不到了"都会在纯粹的
// 换行差异上误报。服务端的切分也做同一件事（pipeline.go 的
// normalizeNewlines），所以归一化之后这里的判断和服务端看到的内容一致。
function normalizeCorpus(corpus) {
  return corpus.map((f) => ({ name: f.name, text: normalizeNewlines(f.text) }));
}

// corpusHash 是语料的指纹，用来回答"这次跑的和上次跑的是不是同一份语料"。
//
// 算法：文件名排序 → 每个文件算 sha256(文件名 + "\n" + 内容) → 把这些
// 十六进制摘要用 \n 连起来再算一次 sha256。
//
// 【为什么先排序、为什么名字要参与哈希】排序让结果与 readdir 的返回顺序
// 无关（不同文件系统、不同平台给出的顺序不一样）。名字参与哈希让"把
// 内容从 a.md 挪到 b.md"这种改动能被识别出来——只算内容的话，重命名
// 文件是看不见的。
export function corpusHash(corpus) {
  const digests = normalizeCorpus(corpus)
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
    .map((f) => sha256Hex(f.name + "\n" + f.text));
  return sha256Hex(digests.join("\n"));
}

// loadDataset 读 JSONL。一行一个问题，加一个问题就是一行 diff，
// 不会像 JSON 数组那样把整个文件重排一遍。
//
// 返回的每一项带上行号和该行的 sha256——报告里要记录"跑的是哪一版数据集"。
export function loadDataset(filePath) {
  const raw = normalizeNewlines(readFileSync(filePath, "utf-8"));
  const lines = raw.split("\n");
  const out = [];
  lines.forEach((line, idx) => {
    const trimmed = line.trim();
    if (trimmed === "") return;
    let obj;
    try {
      obj = JSON.parse(trimmed);
    } catch (err) {
      throw new DatasetError(`dataset.jsonl 第 ${idx + 1} 行不是合法 JSON: ${err.message}`);
    }
    out.push({ line: idx + 1, hash: sha256Hex(trimmed), question: obj });
  });
  if (out.length === 0) {
    throw new DatasetError(`${filePath} 里没有任何问题`);
  }
  return out;
}

// anchorFiles 返回一个锚点落在哪些语料文件里。
//
// 用 head 做一个快速的存在性判断，只在必要时才真的 slice 出上下文——
// 语料只有几 KB，这点微优化其实无所谓，保留它只是为了让"锚点落在两个
// 文件里时要同时报出两个文件名"这条逻辑容易写。
export function anchorFiles(anchor, corpus) {
  return normalizeCorpus(corpus)
    .filter((f) => f.text.includes(anchor))
    .map((f) => f.name);
}

// corpusChunkEstimate 粗略估一下这份语料会被切成多少个分块。
//
// 【为什么只估不精确】它模拟的是 pipeline.go 的贪心装段落，但没有跑 Go
// 代码，所以只用于报告里的量级提示（"语料只有几个分块时 K=5 会退化成
// 全返回"）。真正的分块数以服务端为准，报告里另有一个观测值。
export function corpusChunkEstimate(corpus) {
  let chunks = 0;
  for (const file of corpus) {
    const paras = file.text
      .split(/\n\s*\n/)
      .map((p) => p.trim())
      .filter(Boolean);
    let current = 0;
    for (const p of paras) {
      const n = runeLength(p);
      if (n > MAX_ANCHOR_RUNES) {
        chunks += 1 + Math.ceil(n / MAX_ANCHOR_RUNES); // 超长段落硬切，前面先 flush 一次
        current = 0;
        continue;
      }
      if (current > 0 && current + n + 2 > MAX_ANCHOR_RUNES) {
        chunks += 1;
        current = 0;
      }
      current += (current > 0 ? 2 : 0) + n;
    }
    if (current > 0) chunks += 1;
  }
  return chunks;
}

// validateDataset 把所有"静默错"变成"响亮的错"。返回错误列表，空表示通过。
//
// 三类检查：
//   ① 结构：id 唯一、class 合法、answerable 必须有锚点、should_refuse 不该有锚点
//   ② 长度：锚点不超过 MAX_ANCHOR_RUNES
//   ③ 唯一性：锚点在语料里恰好出现于一个文件——0 次说明写错了字或语料变了，
//      2 次说明这个锚点分辨不出文件，两种情况都会让 recall 的数字失去意义
export function validateDataset(dataset, corpus, opts = {}) {
  const maxAnchorRunes = opts.maxAnchorRunes ?? MAX_ANCHOR_RUNES;
  const errors = [];
  const seenIds = new Map();

  for (const entry of dataset) {
    const q = entry.question;
    const where = `问题 ${q && q.id ? q.id : `<第 ${entry.line} 行>`}`;

    if (typeof q.id !== "string" || q.id.trim() === "") {
      errors.push(`${where}: 缺少 id`);
      continue;
    }
    if (seenIds.has(q.id)) {
      errors.push(`${where}: id 重复（第 ${seenIds.get(q.id)} 行已经用过）`);
    }
    seenIds.set(q.id, entry.line);

    if (q.class !== "answerable" && q.class !== "should_refuse") {
      errors.push(`${where}: class 只能是 answerable 或 should_refuse，实际是 ${JSON.stringify(q.class)}`);
      continue;
    }
    if (typeof q.question !== "string" || q.question.trim() === "") {
      errors.push(`${where}: 缺少 question`);
    }

    const anchors = Array.isArray(q.anchors) ? q.anchors : [];

    if (q.class === "should_refuse") {
      if (anchors.length > 0) {
        errors.push(`${where}: should_refuse 的问题不该有锚点（有锚点说明这个问题其实答得上）`);
      }
      continue;
    }

    if (anchors.length === 0) {
      errors.push(`${where}: answerable 的问题至少要有一个锚点`);
      continue;
    }

    for (const anchor of anchors) {
      const n = runeLength(anchor);
      if (n > maxAnchorRunes) {
        errors.push(
          `${where}: 锚点有 ${n} 个字，超过了 ${maxAnchorRunes} ${PIPELINE_HINT}: ${anchor.slice(0, 30)}…`,
        );
        continue;
      }
      const files = anchorFiles(anchor, corpus);
      if (files.length === 0) {
        errors.push(`${where}: 锚点在语料里一次都没出现（是不是改了语料没改数据集？）: ${anchor}`);
      } else if (files.length > 1) {
        errors.push(
          `${where}: 锚点同时出现在 ${files.length} 个语料文件里，分不出唯一来源: ${files.join(", ")} | ${anchor}`,
        );
      }
    }
  }

  return errors;
}

// resolveAnchors 把每个 answerable 问题映射到它锚点命中的语料文件。
//
// 【为什么不让人手写文件名】数据集里再写一遍文件名，等于把"这段文字属于
// 哪个文件"这个事实存了两份，改语料时必然有一份忘了改。文件名是从锚点
// 反查出来的，只有一份真相。
export function resolveAnchors(dataset, corpus) {
  const out = new Map();
  for (const entry of dataset) {
    const q = entry.question;
    if (q.class !== "answerable") continue;
    const files = new Set();
    for (const anchor of q.anchors ?? []) {
      for (const name of anchorFiles(anchor, corpus)) files.add(name);
    }
    out.set(q.id, [...files].sort());
  }
  return out;
}

export function datasetStats(dataset) {
  const stats = { total: dataset.length, answerable: 0, should_refuse: 0 };
  const byClass = {};
  for (const entry of dataset) {
    const c = entry.question.class;
    byClass[c] = (byClass[c] ?? 0) + 1;
    if (c === "answerable") stats.answerable += 1;
    else if (c === "should_refuse") stats.should_refuse += 1;
  }
  stats.byClass = byClass;
  return stats;
}

// ────────────────────────────────────────────────────────────────
// CLI：把数据集和语料对一遍，打印指纹和每个问题的来源文件
// ────────────────────────────────────────────────────────────────

export function runCli(argv = process.argv.slice(2)) {
  const corpusDir = defaultCorpusDir();
  const datasetPath = defaultDatasetPath();

  const corpus = loadCorpus(corpusDir);
  const dataset = loadDataset(datasetPath);
  const errors = validateDataset(dataset, corpus);
  const stats = datasetStats(dataset);

  const hash = corpusHash(corpus);
  console.log(`语料目录   ${corpusDir}`);
  console.log(`数据集     ${datasetPath}`);
  console.log(`语料指纹   ${hash}`);
  console.log(`语料规模   ${corpus.length} 个文件，估 ${corpusChunkEstimate(corpus)} 个分块（K=5 时参考这个数判断数字有没有意义）`);
  console.log(`问题总数   ${stats.total}（answerable ${stats.answerable} / should_refuse ${stats.should_refuse}）`);

  const resolved = resolveAnchors(dataset, corpus);
  console.log("");
  console.log("id   | class          | 锚点来源");
  console.log("-----|----------------|----------------");
  for (const entry of dataset) {
    const q = entry.question;
    const files = resolved.get(q.id);
    console.log(
      `${q.id.padEnd(4)} | ${String(q.class).padEnd(14)} | ${files ? files.join(", ") : "—"}`,
    );
  }

  if (errors.length > 0) {
    console.error("");
    console.error(`数据集校验失败，共 ${errors.length} 条：`);
    for (const e of errors) console.error(`  - ${e}`);
    return 1;
  }
  console.log("");
  console.log("数据集校验通过。");
  return 0;
}

// 只有直接 `node evals/lib/resolve.mjs` 时才跑 CLI；被 import 时不产生副作用。
const invokedDirectly =
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;

if (invokedDirectly) {
  try {
    process.exit(runCli());
  } catch (err) {
    console.error(err instanceof Error ? err.message : String(err));
    process.exit(1);
  }
}
