// resolve.test.mjs —— 数据集校验和语料指纹的单元测试。
//
// 和 score.test.mjs 一样，这一层不碰网络也不碰数据库：语料是内存里现拼的
// 假语料，断言的每一条都对应一个"如果它坏了，整份 eval 的数字就失去意义"
// 的具体后果。

import { test } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";

import {
  MAX_ANCHOR_RUNES,
  CorpusError,
  DatasetError,
  normalizeNewlines,
  runeLength,
  corpusHash,
  loadCorpus,
  loadDataset,
  anchorFiles,
  validateDataset,
  resolveAnchors,
  datasetStats,
  corpusChunkEstimate,
  defaultCorpusDir,
  defaultDatasetPath,
} from "./resolve.mjs";

// 造一份最小的假语料：两个文件，各自的段落互不相同。
const CORPUS = [
  {
    name: "a.md",
    text: "# 甲\n\n甲文档讲的是 goroutine 的调度，这一段只出现在甲里。\n\n公共段落：这一句在两个文件里都有。\n",
  },
  {
    name: "b.md",
    text: "# 乙\n\n乙文档讲的是 pgvector 的索引，这一段只出现在乙里。\n\n公共段落：这一句在两个文件里都有。\n",
  },
];

function datasetOf(...questions) {
  return questions.map((q, i) => ({
    line: i + 1,
    hash: `hash${i}`,
    question: q,
  }));
}

// ── 锚点唯一性 ──────────────────────────────────────────────────

test("锚点同时出现在两个语料文件里：报错并点名两个文件", () => {
  const ds = datasetOf({
    id: "q001",
    class: "answerable",
    question: "公共段落是什么？",
    anchors: ["公共段落：这一句在两个文件里都有。"],
    facts: [],
  });
  const errors = validateDataset(ds, CORPUS);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /q001/);
  assert.match(errors[0], /a\.md/);
  assert.match(errors[0], /b\.md/);
  assert.match(errors[0], /分不出唯一来源/);
});

test("锚点在语料里一次都没出现：报错提示数据集和语料对不上", () => {
  const ds = datasetOf({
    id: "q002",
    class: "answerable",
    question: "这句话在语料里吗？",
    anchors: ["这句话是我编的，语料里没有。"],
    facts: [],
  });
  const errors = validateDataset(ds, CORPUS);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /一次都没出现/);
});

test("锚点超过 1200 字：拒绝，并指路 pipeline.go", () => {
  const ds = datasetOf({
    id: "q003",
    class: "answerable",
    question: "超长锚点",
    anchors: ["字".repeat(MAX_ANCHOR_RUNES + 1)],
    facts: [],
  });
  const errors = validateDataset(ds, CORPUS);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /1201/);
  assert.match(errors[0], /internal\/knowledge\/pipeline\.go/);
});

test("刚好 1200 字的锚点是允许的（边界不该被误伤）", () => {
  const boundary = "字".repeat(MAX_ANCHOR_RUNES);
  const corpus = [{ name: "c.md", text: boundary }];
  const ds = datasetOf({
    id: "q004",
    class: "answerable",
    question: "边界",
    anchors: [boundary],
    facts: [],
  });
  assert.deepEqual(validateDataset(ds, corpus), []);
});

test("长度按码点数，不按 UTF-16 码元或字节", () => {
  // emoji 是代理对：按码元数是 2，按码点数是 1。口径必须和服务端的
  // utf8.RuneCountInString 一致，否则边界会在中英混排时提前触发。
  assert.equal(runeLength("𝄞"), 1);
  assert.equal(runeLength("汉字"), 2);
});

// ── 结构校验 ────────────────────────────────────────────────────

test("answerable 没有锚点：拒绝", () => {
  const ds = datasetOf({
    id: "q005",
    class: "answerable",
    question: "没有问题锚点",
    anchors: [],
    facts: [],
  });
  assert.match(validateDataset(ds, CORPUS)[0], /至少要有一个锚点/);
});

test("should_refuse 带了锚点：拒绝（说明这题其实答得上）", () => {
  const ds = datasetOf({
    id: "q006",
    class: "should_refuse",
    question: "带锚点的拒答题",
    anchors: ["这一段只出现在甲里"],
    facts: [],
  });
  assert.match(validateDataset(ds, CORPUS)[0], /不该有锚点/);
});

test("id 重复、class 非法、缺 question 都要报出来", () => {
  const ds = datasetOf(
    { id: "q007", class: "answerable", question: "一", anchors: ["这一段只出现在甲里"], facts: [] },
    { id: "q007", class: "answerable", question: "二", anchors: ["这一段只出现在乙里"], facts: [] },
    { id: "q008", class: "unknown", question: "三", anchors: [], facts: [] },
    { id: "q009", class: "answerable", question: "", anchors: ["这一段只出现在甲里"], facts: [] },
  );
  const errors = validateDataset(ds, CORPUS);
  const joined = errors.join("\n");
  assert.match(joined, /id 重复/);
  assert.match(joined, /class 只能是/);
  assert.match(joined, /缺少 question/);
});

// ── 指纹 ────────────────────────────────────────────────────────

test("语料指纹与文件被读到的顺序无关", () => {
  const forward = [...CORPUS];
  const backward = [...CORPUS].reverse();
  assert.equal(corpusHash(forward), corpusHash(backward));
  assert.equal(corpusHash(forward), corpusHash([CORPUS[1], CORPUS[0]]));
});

test("改了内容指纹就变，改了文件名指纹也变", () => {
  const base = corpusHash(CORPUS);
  const changed = CORPUS.map((f, i) => (i === 0 ? { ...f, text: f.text + "多一句。" } : f));
  assert.notEqual(corpusHash(changed), base, "内容变了指纹必须变");

  const renamed = CORPUS.map((f, i) => (i === 0 ? { ...f, name: "renamed.md" } : f));
  assert.notEqual(corpusHash(renamed), base, "只改文件名也必须能看出来");
});

test("指纹和换行风格无关（CRLF 与 LF 得到同一个指纹）", () => {
  const lf = [{ name: "x.md", text: "第一段\n\n第二段\n" }];
  const crlf = [{ name: "x.md", text: "第一段\r\n\r\n第二段\r\n" }];
  assert.equal(corpusHash(lf), corpusHash(crlf));
  assert.equal(normalizeNewlines("a\r\nb\rc"), "a\nb\nc");
});

// ── 反查与统计 ──────────────────────────────────────────────────

test("resolveAnchors 从锚点反查文件名（数据集里不写文件名）", () => {
  const ds = datasetOf(
    { id: "q010", class: "answerable", question: "甲", anchors: ["这一段只出现在甲里"], facts: [] },
    { id: "q011", class: "answerable", question: "乙", anchors: ["这一段只出现在乙里"], facts: [] },
    { id: "q012", class: "should_refuse", question: "拒答", anchors: [], facts: [] },
  );
  const resolved = resolveAnchors(ds, CORPUS);
  assert.deepEqual(resolved.get("q010"), ["a.md"]);
  assert.deepEqual(resolved.get("q011"), ["b.md"]);
  assert.equal(resolved.has("q012"), false, "should_refuse 不参与反查");
});

test("anchorFiles 报告所有命中的文件", () => {
  assert.deepEqual(anchorFiles("公共段落", CORPUS), ["a.md", "b.md"]);
  assert.deepEqual(anchorFiles("不存在", CORPUS), []);
});

test("datasetStats 按 class 计数", () => {
  const ds = datasetOf(
    { id: "q013", class: "answerable", question: "一", anchors: ["这一段只出现在甲里"], facts: [] },
    { id: "q014", class: "should_refuse", question: "二", anchors: [], facts: [] },
  );
  const s = datasetStats(ds);
  assert.equal(s.total, 2);
  assert.equal(s.answerable, 1);
  assert.equal(s.should_refuse, 1);
});

test("分块数估算能看出语料太小的情形", () => {
  assert.ok(corpusChunkEstimate(CORPUS) >= 2);
  const big = [{ name: "big.md", text: Array.from({ length: 10 }, () => "段".repeat(500)).join("\n\n") }];
  assert.ok(corpusChunkEstimate(big) >= 4, "10 个 500 字的段落至少切成 4 块");
});

test("loadCorpus / loadDataset 对坏输入抛出带路径的错误", () => {
  assert.throws(() => loadCorpus("不存在的目录"), CorpusError);
  assert.throws(() => loadDataset("不存在的文件.jsonl"), /ENOENT|no such file/i);
  // 【必须用 fileURLToPath，不能手写 .pathname.slice(1)】
  // `.pathname` 在 Linux 上是 /home/runner/work/...（开头的 / 是路径本身的一部分），
  // 在 Windows 上是 /D:/...（开头的 / 是盘符前的伪前缀）。`.slice(1)` 在 Windows
  // 上正好把那个伪前缀去掉、路径是对的；在 Linux 上却把根目录的 / 也切掉了，
  // 变成一个相对路径 → 文件不存在 → 抛 ENOENT 而不是 DatasetError。
  // 这条断言因此在 Windows 上绿、在 Linux 上红（CI 上就是这样炸的）。
  assert.throws(
    () => loadDataset(fileURLToPath(new URL("./resolve.test.mjs", import.meta.url))),
    DatasetError,
  );
});

// ── 真语料的整体校验 ────────────────────────────────────────────
//
// 这一条是给真正的 evals/corpus + evals/dataset.jsonl 的：它保证仓库里
// 现在这份数据集**现在**是自洽的。改语料忘了改数据集的话，这个测试
// 会红——这比等全量 eval 跑到一半才发现便宜得多。

test("仓库里的真实语料与数据集自洽", () => {
  const corpus = loadCorpus(defaultCorpusDir());
  const dataset = loadDataset(defaultDatasetPath());
  const errors = validateDataset(dataset, corpus);
  assert.deepEqual(errors, [], errors.join("\n"));

  const stats = datasetStats(dataset);
  assert.equal(stats.total, 20);
  assert.equal(stats.answerable, 14);
  assert.equal(stats.should_refuse, 6);

  // 每个 answerable 的题都要能落到至少一个语料文件上
  const resolved = resolveAnchors(dataset, corpus);
  for (const entry of dataset) {
    if (entry.question.class !== "answerable") continue;
    assert.ok(
      resolved.get(entry.question.id).length > 0,
      `${entry.question.id} 没有解析到任何语料文件`,
    );
  }

  // 指纹是内容算出来的，但不该是空的
  assert.match(corpusHash(corpus), /^[0-9a-f]{64}$/);
});
