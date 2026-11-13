# 性能基线

这份文档回答一个问题：**这次改动有没有把某条热路径的常数改坏。**

它记录的是本机上可复现的微基准（micro-benchmark）与前端产物体积，
以及一次真实优化前后的对照。它不是"系统端到端性能报告"——端到端的
延迟（检索 SQL、模型首字延迟、SSE 渲染）需要真的数据库和模型服务才能测，
那部分的测量方式与结果在技术文档与 `docs/evaluation/` 里，本文只覆盖
不依赖外部服务的纯路径。

## 环境与测法

| 项 | 值 |
| --- | --- |
| 机器 | Windows 11，Intel Core i7-14700KF（28 逻辑核） |
| 工具链 | Go 1.26.4 windows/amd64 |
| 采样 | 下表是 `-benchmem -count=3`（优化对照为 `count=4`）的中位数；`make bench` 本身默认每项跑一次，够看量级，要比较请显式加 `-count` |
| 负载 | 测量时有其他进程在跑（前端构建/测试），绝对值仅供本机对照；跨机器比较请重跑 |
| 前置 | 不启动数据库、不配置模型服务、不联网 |

重测：

```bash
make bench                 # 全部纯函数基准
make bench-dedupe          # 只跑近重复去重（本文第 3 节的优化对象）
go test -run "^$" -bench "DedupeNearDuplicates" -benchmem -count=4 ./internal/retrieval/
```

## 1. 基准总览（v6.2.0，2026-11-14）

共 **12 条生产基准 + 2 条对照基准**：生产基准覆盖六个包的热路径；两条对照是
`DedupeNearDuplicates_Top20`（大 topK 场景，只有新实现数据）与
`DedupeNearDuplicates_LegacyReference`（旧实现，只在测试里存在，用来看优化幅度）。

数字格式：中位 `ns/op` / `B/op` / `allocs/op`。粗体是与"每条请求/每份分块"
直接相关的那几条，其余是防回归的护栏。

| 基准 | 覆盖的路径 | 结果 |
| --- | --- | --- |
| `tokenize/Text_Paragraph` | worker 每分块切词（~1KB 中英混排） | **22.0 µs** / 40.6 KB / 466 allocs |
| `tokenize/Text_Query` | 用户提问切词（~34 字） | **1.34 µs** / 2.0 KB / 31 allocs |
| `tokenize/Tokens_Paragraph` | SimHash 用的不去重切分 | 17.9 µs / 26.0 KB / 456 allocs |
| `tokenize/Query` | 拼 `to_tsquery` 表达式 | 2.0 µs / 2.4 KB / 33 allocs |
| `grounding/Verify` | 回答结束时的句子级引用落地检查 | **9.15 µs** / 9.8 KB / 152 allocs |
| `retrieval/DedupeNearDuplicates` | 近重复去重（topK=5，50 候选） | **162 µs** / 48.7 KB / 22 allocs（优化后） |
| `retrieval/DedupeNearDuplicates_Top20` | 去重后凑 20 条（对比/写作场景） | **1.03 ms** / 218.7 KB / 92 allocs（仅有新实现，旧实现无同口径数据） |
| `retrieval/FuseRRF` | 向量 + 词法两路 RRF 融合（各 50 条） | 16–30 µs / 33.0 KB / 134 allocs（区间受并行构建负载影响） |
| `knowledge/ContentFingerprint` | 上传去重的 SimHash（~20KB 正文） | **305 µs** / 203.3 KB / 3,376 allocs |
| `knowledge/ParseAndChunk` | 解析产物切分（~48KB 纯文本） | **173 µs** / 264.8 KB / 296 allocs |
| `knowledge/BuildCitationGraph` | 20 篇论文构图 + PageRank | 1.8 µs / 4.1 KB / 14 allocs |
| `platform/CursorRoundTrip` | keyset 游标编解码 | 227 ns / 512 B / 7 allocs |
| `visualize/Build` | CSV（500 行）→ Mermaid/TikZ/Matplotlib + Caption | **3.1–3.4 ms** / 14.6 MB / 29,231 allocs |

读法上要注意两件事：

1. **微基准不是端到端。** 例如 `tokenize/Text_Query` 的 1.34 µs 只说明切词
   本身很便宜，一次问答的延迟几乎全在检索 SQL 与模型首字上（技术文档里
   给的 154ms / P95 178ms 是全链路数字，只有连上数据库与模型才能复测）。
2. **allocs/op 比 ns/op 更值得盯。** 它和机器无关，回归时也更稳定；
   下面那次去重优化的主要收益就落在这一列上。

## 2. 一次真实优化：近重复去重（v6.2.0）

### 问题

`internal/retrieval/dedupe.go` 在混合检索排序之后做 5-gram Jaccard 近重复抑制。
旧实现给每个 5-gram 建一个**新字符串**再塞进 `map[string]struct{}`：
基准语料里一块约 800 字，会产生约 800 个短字符串，一次去重里累计约 6 万个
短字符串（真实 top-50 语料更长，量级还会上去）。
它跑在每条检索请求的主路径上，分配压力最终表现为 GC 与尾延迟。

### 做法

把 shingle 集合的表示从"字符串集合"换成"**排序去重后的 64 位 FNV 哈希切片**"：

- 归一化（小写、只留字母/数字）的规则不变；
- 每个 5-rune 窗口算一个 `uint64`，排序后去重；
- 集合交并退化成两个有序切片的双指针合并，`jaccard` 不再分配内存。

**为什么碰撞可以接受**：一次去重里出现的不同 5-gram 量级约 6×10⁴，
两两碰撞的期望约 `C(6e4,2)/2^64 ≈ 1e-10`；而 0.85 这个阈值本身是经验值，
差的几个百分点远大于哈希带来的误差。

### 结果（同机、同进程、同语料、limit=5，各 count=5 取中位数）

对照跑的是 `BenchmarkDedupeNearDuplicates`（新实现）与
`BenchmarkDedupeNearDuplicates_LegacyReference`（旧实现，只在测试里保留）：

| 指标 | 旧实现 | 新实现 | 变化 |
| --- | ---: | ---: | ---: |
| ns/op | 212,455 | 161,184 | **−24.1%** |
| B/op | 191,001 | 48,664 | **−74.5%** |
| allocs/op | 3,354 | 22 | **−99.3%** |

（更早一次单独跑旧实现时得到 203,333 ns / 186,564 B / 3,319 allocs，与上表
同一量级；差异来自当时机器上有其它构建任务在跑，所以本文只采用同一进程内
相邻跑出来的对照值。）

旧实现还在 `dedupe_spec_test.go` 里保留了一份**只在测试中运行的对照实现**
（`BenchmarkDedupeNearDuplicates_LegacyReference`），这样"快了"是可复现的，
而不是一句提交信息。等价性由 `TestShingleHashes_MatchesStringDefinition` 与
`TestShingleHashes_JaccardMatchesReference` 对多组语料断言（集合大小、
Jaccard 数值与字符串定义一致）。

**没换来什么**：Top20 场景仍是毫秒级（1.03 ms）——那里真正的成本是
`limit × 候选数` 次集合比较，属于算法层面的 O(n·k)，换表示只能改常数。
要让大 topK 也便宜，得换成 MinHash/LSH 之类的近似方案，但那会引入新的
误判面，暂不在本轮范围。

## 3. 前端产物体积（v6.2.0）

构建命令：`pnpm --filter web build`（产物 `go:embed` 进 `apps/api/web/`）。

| 指标 | 值 |
| --- | ---: |
| 构建产物总大小 | 2.675 MB（58 个文件，vite build 471ms） |
| 首屏（entry + modulepreload + stylesheet，raw） | 604,457 B |
| 首屏（gzip） | 179,100 B |
| 其中 JS / CSS | 511,918 B（gzip 162,959）/ 92,539 B（gzip 16,141） |
| 入口 chunk `index-*.js` | 40,962 B（gzip 11,864 B） |
| `react-*.js` / `lib-*.js` / `radix-*.js` | 218,840 / 150,399 / 97,349 B |
| 阅读器 `ReaderPage-*.js`（懒加载） | 444,056 B（gzip 132,324 B） |
| PDF worker（懒加载） | 1,178,002 B（gzip 363,302 B） |

两条结论：

- **pdfjs 没有进首屏。** `ReaderPage` 与 `pdf.worker.min-*.js` 都是路由级懒加载
  的产物，只有进阅读器才下载；`vite.config.ts` 里的 `codeSplitting` 分组
  只覆盖 `$initial` 模块，所以懒加载专属依赖不会被拽回首屏。
- **体积基线的判据**：首屏 gzip 涨 4% 以上、单个业务 chunk 涨 8% 以上要说明原因。
  解析脚本：`scripts/bundle-report.mjs`（`make bundle-report`），基线 JSON 在
  `docs/perf/bundle-baseline.json`。

## 4. 已知热点（不在本轮修复范围）

| 热点 | 现状 | 后续 |
| --- | --- | --- |
| `visualize.Build` | 500 行 CSV 一次调用 14.6 MB 分配、29k allocs | 它不在请求主路径上（用户点一次生成一次），但分配量偏大；下一步是把表格解析与代码模板生成改成按列预分配 / `strings.Builder` 复用 |
| `knowledge.ContentFingerprint` | 20KB 正文 3,376 allocs | 与 `tokenize.Tokens` 共用切分后仍逐 token 新建字符串；若上传吞吐成为瓶颈再做流式哈希 |
| 大 topK 去重 | Top20 1.03 ms | 需要 MinHash/LSH 级别的方案，单独评估 |

## 5. 门禁与压测入口

- **基准回归门禁**：`make bench-gate`（`node scripts/bench-gate.mjs`）把 §1 的这批
  benchmark 与 `benchmarks/baseline.json` 比较：allocs/op 超过基线观测上界即失败（确定性基准等价于上涨即失败）、B/op > +5% 失败、
  ns/op > +30% 且绝对增量 >200ns 才失败（阈值与退出码的完整说明见 `docs/testing.md`
  的「基准回归门禁」一节）。基线用 `node scripts/bench-gate.mjs --write` 重写，
  换机器或升级 Go 时要重写并在提交信息里说明。
- **并发压测模拟**：`make loadtest`（`node ops/loadtest/stream-load.mjs`）用 mock SSE
  服务测压测器与 SSE 分帧路径的容量；真实后端用 `--target URL` 指向。方法论与三档
  实测结果见 [docs/loadtest-report.md](loadtest-report.md)。
