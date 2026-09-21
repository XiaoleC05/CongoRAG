// score.test.mjs —— 指标算术和 SSE 解析的单元测试。
//
// 这些测试就是 CI 里跑的那一半（见 evals/README.md）：输入是 evals/fixtures/
// 下手写的 .sse 帧序列，全程不碰网络、不碰数据库、不调模型，所以它们
// 该有的性质是"永远要么全绿要么指出一个真 bug"，而不是"今天上游抽风了
// 所以红一片"。

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  newParserState,
  parseSSE,
  parseSSEString,
  payloadOf,
  citationsOf,
  answerOf,
  errorOf,
  matchAnchor,
  isRefusal,
  evaluateFacts,
  scoreQuestion,
  questionPassed,
  median,
  summarise,
  aggregatePasses,
  compareReports,
  buildReport,
  formatReport,
  DEFAULT_REFUSAL_MARKERS,
} from "./score.mjs";

// ── fixture 装载 ────────────────────────────────────────────────

function fixtureText(name) {
  return readFileSync(new URL(`../fixtures/${name}`, import.meta.url), "utf-8");
}

// loadRun 模拟驱动拿到一次完整的流之后应该喂给打分函数的东西。
function loadRun(name) {
  const frames = parseSSEString(fixtureText(name));
  const err = errorOf(frames);
  return {
    frames,
    citations: citationsOf(frames),
    answer: answerOf(frames),
    errored: err !== null,
    errorType: err ? err.type : null,
  };
}

// 锚点用的是 fixture 里逐字出现的片段——和数据集里的锚点是同一个写法，
// 都是"内容的子串"，不是 id。
const ANCHOR_HALFVEC = "半精度把每个维度的存储从四字节压到两字节";
const ANCHOR_HNSW = "它把向量组织成多层图，查询时从稀疏的上层快速跳到目标区域附近";

const Q_HALFVEC = {
  id: "t001",
  class: "answerable",
  question: "向量列为什么用半精度？",
  anchors: [ANCHOR_HALFVEC],
  facts: ["halfvec"],
};

const Q_HNSW = {
  id: "t002",
  class: "answerable",
  question: "HNSW 怎么找近邻？",
  anchors: [ANCHOR_HNSW],
  facts: ["HNSW"],
};

const Q_REFUSE = {
  id: "t003",
  class: "should_refuse",
  question: "支持 SAML 单点登录吗？",
  anchors: [],
  facts: [],
};

// ── 帧格式 ──────────────────────────────────────────────────────

test("整段读一次：citation/token/done 各自解析出来", () => {
  const run = loadRun("full-hit.sse");
  assert.equal(run.frames.length, 5);
  assert.deepEqual(
    run.frames.map((f) => f.event),
    ["citation", "token", "token", "token", "done"],
  );
  // 【线路上的 payload 是包了一层的】conversation.emitEvent 写的是
  // {type, data}，真正的内容在 .data 里。这条断言就是钉住这个形状的：
  // 如果哪天包装层被去掉，这个测试必须跟着改，而不是悄悄解析出 undefined。
  assert.equal(run.frames[0].json.type, "citation");
  assert.equal(run.citations.length, 1);
  assert.match(run.citations[0].snippet, /halfvec/);
  assert.equal(run.citations[0].filename, "02-vector-retrieval.md");
  assert.equal(run.answer, "向量列用的是 halfvec，也就是半精度浮点向量。");
});

test("payloadOf 对没有 data 的帧返回空对象，不抛异常", () => {
  const frames = [{ id: "1", event: "done", data: "{}", json: {} }];
  assert.deepEqual(payloadOf(frames[0]), {});
  assert.deepEqual(payloadOf(undefined), {});
});

test("心跳注释行不产生事件", () => {
  const withHeartbeat = loadRun("heartbeat.sse");
  assert.equal(withHeartbeat.frames.length, 3);
  assert.deepEqual(
    withHeartbeat.frames.map((f) => f.event),
    ["citation", "token", "done"],
  );
  // 心跳不占号：id 仍然是 1/2/3
  assert.deepEqual(
    withHeartbeat.frames.map((f) => f.id),
    ["1", "2", "3"],
  );
});

test("一帧跨两次 read 与一次读完结果完全相同", () => {
  // 【为什么每个切点都试一遍】只挑一个切点的话，恰好切在帧边界上就会
  // 假绿——而"切在边界上"不是这个测试想证明的性质。逐字节穷举能保证
  // 中间的那一小段（有 id 没 event、有 event 没 data…）也被覆盖到。
  for (const name of ["partial-hit.sse", "error-no-id.sse", "heartbeat.sse"]) {
    const text = fixtureText(name);
    const expected = parseSSEString(text);
    const expectedCursor = parseSSE(text, newParserState()).state.lastEventId;
    for (let cut = 1; cut < text.length; cut += 1) {
      let state = newParserState();
      const first = parseSSE(text.slice(0, cut), state);
      state = first.state;
      const second = parseSSE(text.slice(cut), state);
      state = second.state;
      const frames = [...first.frames, ...second.frames];
      assert.deepEqual(
        frames.map((f) => ({ id: f.id, event: f.event, data: f.data })),
        expected.map((f) => ({ id: f.id, event: f.event, data: f.data })),
        `${name} 在 offset ${cut} 处切开后解析结果不一致`,
      );
      assert.equal(state.buffer, "", `${name} offset ${cut} 之后缓冲区应该排空`);
      assert.equal(
        state.lastEventId,
        expectedCursor,
        `${name} offset ${cut} 之后游标应该和一次读完的结果一致`,
      );
    }
  }
});

test("无 id 的 error 帧被识别为错误，且不推进续传游标", () => {
  const text = fixtureText("error-no-id.sse");
  const run = loadRun("error-no-id.sse");
  assert.ok(run.errored, "应该识别出这是一次出错");
  assert.equal(run.errorType, "internal_error");

  // 只读到 error 帧为止：游标必须停在上一条真事件（id=1）上。
  // 如果这里变成了 0 或者被当成了新事件号，断线续传会从头跳过一批事件。
  const cut = text.indexOf("\n\n", text.indexOf("event: error")) + 2;
  const mid = parseSSE(text.slice(0, cut), newParserState());
  assert.deepEqual(
    mid.frames.map((f) => f.event),
    ["citation", "error"],
  );
  assert.equal(mid.state.lastEventId, 1);
  assert.equal(mid.frames[1].id, null);

  // 继续读完剩下的部分，游标正常推到 3
  const rest = parseSSE(text.slice(cut), mid.state);
  assert.equal(rest.state.lastEventId, 3);
});

test("带 id 的 error 帧照样推进游标（它是真的落了库的事件）", () => {
  const frames = parseSSEString(fixtureText("errored.sse"));
  const err = errorOf(frames);
  assert.equal(err.type, "upstream_error");
  assert.equal(err.id, "1");
  const state = parseSSE(fixtureText("errored.sse"), newParserState()).state;
  assert.equal(state.lastEventId, 1);
});

// ── 单题打分 ────────────────────────────────────────────────────

test("第一条引用就命中：recall@1 = 1，MRR = 1", () => {
  const run = loadRun("full-hit.sse");
  const rec = scoreQuestion(Q_HALFVEC, run);
  assert.equal(rec.hitRank, 1);
  assert.equal(rec.recallAt[1], 1);
  assert.equal(rec.recallAt[3], 1);
  assert.equal(rec.recallAt[5], 1);
  assert.equal(rec.refused, false);
  assert.equal(rec.facts.hit, true);
  assert.equal(rec.ok, true);
});

test("第三条引用才命中：recall@1 = 0、recall@3 = 1、MRR = 1/3", () => {
  const run = loadRun("partial-hit.sse");
  const rec = scoreQuestion(Q_HNSW, run);
  assert.equal(rec.hitRank, 3);
  assert.equal(rec.recallAt[1], 0);
  assert.equal(rec.recallAt[3], 1);
  assert.equal(rec.recallAt[5], 1);

  const s = summarise([rec], { kValues: [1, 3, 5] });
  assert.equal(s.metrics["context_recall@1"].value, 0);
  assert.equal(s.metrics["context_recall@3"].value, 1);
  assert.ok(Math.abs(s.metrics["mrr"].value - 1 / 3) < 1e-12);
});

test("一条引用都没有：全部计 0，不是 NaN、不除零", () => {
  const run = loadRun("no-citations.sse");
  assert.equal(run.citations.length, 0);
  const rec = scoreQuestion(Q_HALFVEC, run);
  assert.equal(rec.hitRank, null);
  assert.equal(rec.citationCount, 0);
  for (const k of [1, 3, 5]) assert.equal(rec.recallAt[k], 0);
  assert.equal(rec.ok, false, "没有引用支撑的题不算通过");

  const s = summarise([rec]);
  assert.equal(s.metrics["context_recall@1"].value, 0);
  assert.equal(s.metrics["context_recall@3"].value, 0);
  assert.equal(s.metrics["context_recall@5"].value, 0);
  assert.equal(s.metrics["mrr"].value, 0);
  assert.ok(Number.isFinite(s.metrics["mrr"].value));
  assert.equal(s.metrics["median_citations"].value, 0);
});

test("answerable 被误拒：拒答率那一项要能抓到", () => {
  const run = loadRun("wrong-refusal.sse");
  const rec = scoreQuestion(Q_HALFVEC, run);
  assert.equal(rec.refused, true);
  assert.equal(rec.ok, false);
  // 引用其实是命中了的（snippet 里没有半精度那句，所以这里没命中）——
  // 这道 fixture 的引用是 pgvector 那段，换成它自己的锚点就能同时验证
  // "检索成功但回答误拒"这种最难看的组合。
  const rec2 = scoreQuestion(
    { ...Q_HALFVEC, anchors: ["pgvector 是 PostgreSQL 的向量扩展"] },
    run,
  );
  assert.equal(rec2.hitRank, 1);
  assert.equal(rec2.refused, true);
  assert.equal(rec2.ok, false, "引用命中了但回答误拒，依然不算通过");

  const s = summarise([rec2]);
  assert.equal(s.metrics["wrongly_refused_rate"].value, 1);
  assert.equal(s.metrics["refusal_accuracy"].denominator, 0, "没有 should_refuse 的题，分母是 0");
  assert.equal(s.metrics["refusal_accuracy"].value, 0, "分母为 0 时返回 0，不是 NaN");
});

test("should_refuse 的题：拒答了才算对", () => {
  const refused = scoreQuestion(Q_REFUSE, {
    citations: [],
    answer: "资料中没有提到 SAML 相关内容。",
  });
  assert.equal(refused.ok, true);

  const answered = scoreQuestion(Q_REFUSE, {
    citations: [],
    answer: "支持，在设置页开启 SAML 即可。",
  });
  assert.equal(answered.ok, false);

  const s = summarise([refused, answered]);
  assert.equal(s.metrics["refusal_accuracy"].value, 0.5);
});

test("出错的问题记 0 分，但单独计数", () => {
  const run = loadRun("errored.sse");
  const rec = scoreQuestion(Q_HALFVEC, run);
  assert.equal(rec.errored, true);
  assert.equal(rec.errorType, "upstream_error");
  assert.equal(rec.ok, false);
  const s = summarise([rec]);
  assert.equal(s.counts.errored, 1);
  assert.equal(s.counts.answerable, 1);
  assert.equal(s.metrics["context_recall@1"].value, 0, "出错的题留在分母里，否则崩掉反而会拉高分数");
});

test("空记录集：每个比率都返回 0，不是 NaN", () => {
  const s = summarise([]);
  assert.equal(s.counts.total, 0);
  for (const [key, m] of Object.entries(s.metrics)) {
    assert.ok(Number.isFinite(m.value), `${key} 应该是有限数`);
  }
});

// ── 汇总与报告 ──────────────────────────────────────────────────

test("观测到的片段数和 K 的关系会被算出来（语料太小的警告线）", () => {
  const a = scoreQuestion(Q_HALFVEC, loadRun("full-hit.sse"));
  const b = scoreQuestion(Q_HNSW, loadRun("partial-hit.sse"));
  const s = summarise([a, b]);
  // full-hit 1 条 + partial-hit 3 条，共 4 个不同片段
  assert.equal(s.metrics["observed_unique_snippets"].value, 4);
  assert.equal(s.metrics["corpus_covers_topk"].value, 0, "4 <= K=5，应当判定为覆盖不住");
});

test("检索指标和生成指标落在不同的 block 里", () => {
  const s = summarise([scoreQuestion(Q_HALFVEC, loadRun("full-hit.sse"))]);
  assert.equal(s.metrics["context_recall@1"].block, "retrieval");
  assert.equal(s.metrics["mrr"].block, "retrieval");
  assert.equal(s.metrics["facts_hit_rate"].block, "generation");
  assert.equal(s.metrics["refusal_accuracy"].block, "generation");
  assert.equal(s.metrics["median_citations"].block, "info");

  const text = formatReport(s);
  assert.match(text, /检索指标（确定性/);
  assert.match(text, /生成指标（随机/);
});

test("median：奇数取中间，偶数取平均，空集返回 null", () => {
  assert.equal(median([3, 1, 2]), 2);
  assert.equal(median([4, 1, 3, 2]), 2.5);
  assert.equal(median([]), null);
});

test("facts 大小写不敏感，缺哪个要报出来", () => {
  assert.deepEqual(evaluateFacts("向量列用的是 halfvec", ["HALFVEC"]).hit, true);
  const miss = evaluateFacts("向量列用的是半精度", ["halfvec"]);
  assert.equal(miss.hit, false);
  assert.deepEqual(miss.missing, ["halfvec"]);
  assert.equal(evaluateFacts("随便", []).evaluated, false, "没有 facts 的题不参与命中率");
});

test("拒答判据：空回答不算拒答", () => {
  assert.equal(isRefusal("", DEFAULT_REFUSAL_MARKERS), false);
  assert.equal(isRefusal("没有相关内容", DEFAULT_REFUSAL_MARKERS), true);
  assert.equal(isRefusal("halfvec 是半精度向量", DEFAULT_REFUSAL_MARKERS), false);
});

test("anchor 匹配：没有锚点或者没有引用都返回 null", () => {
  assert.equal(matchAnchor([{ snippet: "x" }], []), null);
  assert.equal(matchAnchor([], [ANCHOR_HALFVEC]), null);
  assert.equal(matchAnchor([{ snippet: ANCHOR_HALFVEC }], [ANCHOR_HALFVEC]), 1);
});

test("questionPassed 的四种组合", () => {
  const base = { errored: false, class: "answerable", refused: false, facts: { hit: true }, hitRank: 1 };
  assert.equal(questionPassed(base), true);
  assert.equal(questionPassed({ ...base, errored: true }), false);
  assert.equal(questionPassed({ ...base, refused: true }), false);
  assert.equal(questionPassed({ ...base, hitRank: null }), false);
  assert.equal(questionPassed({ ...base, facts: { hit: false } }), false);
});

// ── 基线与 diff ─────────────────────────────────────────────────

function fakeReport(questionResults, overrides = {}) {
  const s = summarise(
    questionResults.map((r) => scoreQuestion(r.q, r.run)),
  );
  return buildReport({
    summary: s,
    records: questionResults.map((r) => scoreQuestion(r.q, r.run)),
    corpusHash: "deadbeef",
    datasetHash: "cafebabe",
    datasetLines: [{ id: "t001", hash: "00" }],
    gitHead: "000000000000",
    gitClean: true,
    apiBaseUrl: "http://127.0.0.1:3210",
    embeddingModel: "BAAI/bge-m3",
    ...overrides,
  });
}

test("compareReports：单题从通过翻成不通过要被列出来", () => {
  const good = fakeReport([{ q: Q_HALFVEC, run: loadRun("full-hit.sse") }]);
  const bad = fakeReport([{ q: Q_HALFVEC, run: loadRun("no-citations.sse") }]);
  const diff = compareReports(good, bad);
  assert.equal(diff.hasPrev, true);
  assert.deepEqual(diff.flipped.pass_to_fail, ["t001"]);
  assert.deepEqual(diff.flipped.fail_to_pass, []);
  const recall = diff.deltas.find((d) => d.key === "context_recall@1");
  assert.equal(recall.prev, 1);
  assert.equal(recall.cur, 0);
  assert.equal(recall.worse, true, "recall 变低就是变差");
});

test("compareReports：被误拒率上升是变差（方向相反的那一项）", () => {
  const ok = fakeReport([{ q: Q_HALFVEC, run: loadRun("full-hit.sse") }]);
  const worse = fakeReport([{ q: Q_HALFVEC, run: loadRun("wrong-refusal.sse") }]);
  const diff = compareReports(ok, worse);
  const m = diff.deltas.find((d) => d.key === "wrongly_refused_rate");
  assert.equal(m.direction, "lower");
  assert.equal(m.worse, true);
});

test("compareReports：遇到“尚未产生”的基线要拒绝对比，而不是按 0 算", () => {
  const cur = fakeReport([{ q: Q_HALFVEC, run: loadRun("full-hit.sse") }]);
  const diff = compareReports({ status: "not-yet-produced", metrics: null }, cur);
  assert.equal(diff.hasPrev, false);
  assert.match(diff.reason, /尚未产生真实数据/);
  const none = compareReports(null, cur);
  assert.equal(none.hasPrev, false);
});

test("compareReports：数据集增删题目也能看出来", () => {
  const a = fakeReport([{ q: Q_HALFVEC, run: loadRun("full-hit.sse") }]);
  const b = fakeReport([
    { q: Q_HALFVEC, run: loadRun("full-hit.sse") },
    { q: Q_HNSW, run: loadRun("partial-hit.sse") },
  ]);
  assert.deepEqual(compareReports(a, b).flipped.only_cur, ["t002"]);
  assert.deepEqual(compareReports(b, a).flipped.only_prev, ["t002"]);
});

test("aggregatePasses：多遍跑的极差和稳定性", () => {
  const p1 = summarise([scoreQuestion(Q_HALFVEC, loadRun("full-hit.sse"))]);
  const p2 = summarise([scoreQuestion(Q_HALFVEC, loadRun("no-citations.sse"))]);
  const agg = aggregatePasses([p1, p2]);
  assert.equal(agg["context_recall@1"].stable, false);
  assert.equal(agg["context_recall@1"].mean, 0.5);
  assert.equal(agg["context_recall@1"].min, 0);
  assert.equal(agg["context_recall@1"].max, 1);
});

test("buildReport 记下语料指纹、git 状态和数据集行哈希", () => {
  const report = fakeReport([{ q: Q_HALFVEC, run: loadRun("full-hit.sse") }]);
  assert.equal(report.corpusHash, "deadbeef");
  assert.equal(report.gitClean, true);
  assert.equal(report.datasetLines.length, 1);
  assert.equal(report.questions[0].id, "t001");
  assert.equal(report.questions[0].ok, true);
});
