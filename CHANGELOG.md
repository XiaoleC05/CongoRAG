# 更新日志

本文件记录每个发布版本里**用户需要知道**的变化。格式参照
[Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循
`MAJOR.MINOR`（热修才出现 `MAJOR.MINOR.PATCH`）。

## 什么时候必须写

**必须写**（写进下面的 `### 行为变更` 一节）：

- 端点的增删、响应字段的增删、状态码语义的变化
- 环境变量的新增 / 改名 / 默认值变化
- 默认值变化（包括契约里的 `default`）
- 错误分类变化（同一个错误从一种 `Problem.type` 变成另一种）
- SSE 帧格式的变化
- 需要人工干预的迁移（比如"升级前必须先跑 `make migrate-up`"）
- 一切"升级后得做点什么"的变化

**不必写**：纯内部重构、测试增删、注释与文档改写、CI 配置调整、不改运行时
行为的补丁级依赖升级。

**判据**：如果升级之后用户什么都不做可能会遇到问题，就必须写。

发布脚本（`scripts/release.mjs`）以本文件为前提——找不到对应的小节会直接
拒绝发布，见 [`docs/releasing.md`](docs/releasing.md)。

---

## [6.7.1] - 2026-11-14

最终验收补丁。**无代码行为变更**；修的是文档与部署示例中"照抄会失败/口径不一致"
的四处问题（QA 最终验收 S2/S3）。

### 修复

- **Caddy 示例指令位置**：`deployments/production/README.md` 里
  `header_up Host` 与 `flush_interval -1` 原先写在站点块顶层（Caddy 会报
  unrecognized directive），已移入 `reverse_proxy { ... }` 子块——这是"照抄示例
  会被 OriginCheck 403 / SSE 被缓冲"的直接原因。
- **镜像 tag 对齐**：README 的 `docker build -t congorag-api:6.7.1` 与
  `deployments/production/.env.example` 的 `CONGORAG_IMAGE_TAG=6.7.1` 统一，
  避免按 README 三步走时 compose 找 `congorag-api:cup` 不存在。
- **启动包 PDF 限制写清**：startup README 与 `docs/deployment.md` §3.1 注明
  启动包不含 pdf-service，`.pdf` 上传会 failed；演示 PDF 请用 production compose。
- **nginx 示例语法**：`listen 443 ssl http2` 拆成 `listen 443 ssl` + `http2 on`，
  并给 upstream keepalive 补 `proxy_set_header Connection ""`。
- **质量基线更新到最终快照**：`docs/quality-baseline.md` 改为 v6.7.1
  （835 用例/33 skip、32 文件 214 前端用例、13 E2E、首屏 607,453 B/179,804 B gzip），
  §5 覆盖率与 §9 Prompt 快照标注采集版本。
- **文档数字订正**：iterations.md 的 runbook/架构文档行数与环境变量数、
  performance.md 的最终产物体积、testing.md 的 E2E 措辞、CI job 编号顺序、
  CHANGELOG 6.6/6.7 的两处表述与实现对齐。

### 升级说明

- 无数据库/契约 schema 变更；契约 `info.version` 与 tag 同步到 6.7.1；
- 部署侧只看两行：Caddy 的 `header_up`/`flush_interval` 必须在
  `reverse_proxy` 块内；镜像 tag 要同时改 `docker build -t` 与 `.env`。

## [6.7.0] - 2026-11-14

部署文档与评审/答辩材料版本。**无代码行为变更**；补齐"别人怎么部署、怎么验、
怎么问不倒"的那层材料，并修正三处部署配置与文档不一致。

### 变更

- **新增 docs/deployment.md（生产 runbook）**：三份 compose 的差异、完整环境变量表
  （含 v6.4.0 四个限流变量）、反向代理约束（Host 必须转发为回环、XFF 必须
  覆盖式、SSE 必须关缓冲）、首次部署/升级/回滚、备份恢复（DB + 数据卷 +
  master.key）、监控探针与日志字段、9 条故障 playbook、命令级验收清单。
- **新增 docs/architecture.md**：系统全景与进程职责、分层与 ADR-001 取舍、
  包依赖图（与 internal/arch_test.go 的允许边表一致）、5 条关键请求流、
  数据模型概要、ADR-001~010 索引、关键不变式、已知架构债。
- **新增 docs/defense/**：答辩问答（14 问，每题附证据位置）、7 分钟 Demo 脚本
  （逐段对应评分 a–f）、评分证据矩阵（细项 → 代码/测试/文档/截图/提交）。
- **新增 docs/iterations.md**：六轮迭代（v6.2.0 → v6.7.0）的审计发现、
  方案共识、落地与清理、验证结果、提交索引。
- **修正部署配置**：
  - `deployments/production/README.md` 的 Caddy 示例补 `header_up Host`
    与 `flush_interval -1`——照抄旧示例会被 OriginCheck 403、SSE 被缓冲；
  - production compose 补传限流 4 项 + 检索去重 + 主密钥变量，startup compose
    补传限流 4 项（此前只改 `.env` 不会进容器）；
  - 新增 `deployments/production/nginx.conf.example`（覆盖式 XFF、
    `proxy_buffering off`、上传上限对齐）。
- **修正过期注释**：`apps/api/internal/app/shutdown.go` 里"SSE 心跳不存在"
  的说法已过期（v6.4.0 起有 15s 空闲心跳）。
- **提交材料目录说明**：`docs/submission/README.md` 注明该目录是省赛
  v6.1.0 材料副本，当前主线为 v6.7.0，新增证据见 defense/architecture/deployment。
- **README 收口**：版本 6.7.0、docs 索引补 architecture/deployment/iterations/
  defense/loadtest，目录结构同步。

### 升级说明

- 无数据库/契约 schema 变更；契约 `info.version` 与 tag 同步到 6.7.0；
- 部署侧若沿用旧 Caddy 示例，请按新示例补 Host 覆盖与缓冲关闭两行。

## [6.6.0] - 2026-11-14

自动化测试与压测版本。**无运行时行为变更、无端点/环境变量/数据库变更**；
新增的是"怎么证明它是对的"那部分能力。

### 变更

- **Playwright E2E（13 个用例）接入 CI**：`web/e2e/`（全量 mock `/api/v1/**`，
  外部域名 abort）覆盖桌面 9 页 + 窄屏 3 场景 + 命令面板交互；断言 h1、
  无 console/pageerror、无横向溢出、未 mock 接口为零；CI 新增 `e2e` job
  （chromium，失败上传 test-results）。首次跑需
  `pnpm --filter web exec playwright install chromium`。
- **benchmark 回归门禁**：`scripts/bench-gate.mjs` + `benchmarks/baseline.json`，
  解析 14 条基准的 ns/B/allocs；allocs/B 用「本次 min vs 基线 max」、ns 用
  min-vs-min（吸收采样抖动），allocs 严格、B/op +5%、
  ns/op +30% 且 >200ns 才失败（共享 runner 降噪）；支持 `--write` 重写基线。
  新增 `make bench-gate` 与 CI 的 advisory `bench` job。
- **SSE 高并发压测模拟**：`ops/loadtest/stream-load.mjs`（零依赖）内置 mock
  SSE 服务与和前端一致的分帧逻辑，25/50/100 三档输出成功率、TTFB 分位、
  events/s、RSS；`--target URL` 可打真实 api。方法与三档实测见
  [docs/loadtest-report.md](docs/loadtest-report.md)、`make loadtest`。
- **Go SSE benchmark**：`BenchmarkSSEEmit`（161.7 ns/64B/4 allocs）与
  `BenchmarkSSESinkConcurrent`（8/32 goroutine，锁竞争 +20.7%/+33.4%）。
- **测试文档**：`docs/testing.md` 增加 E2E/基准门禁/压测模拟三节；
  `docs/performance.md` 增加 bench-gate 用法。

### 升级说明

- 无数据库/契约 schema 变更；契约 `info.version` 与 tag 同步到 6.6.0；
- CI 从 7 个 job 变 8 个（新增 e2e；bench 为 advisory）；
- 开发者本机需要时再装 Playwright 浏览器，不影响原有 `make test`。

## [6.5.0] - 2026-11-14

UI/UX 与响应式版本。**无端点/环境变量/数据库变更**；模型输出与接口语义不变。

### 变更

- **命令面板（⌘K / Ctrl+K）**：跨知识库、会话、Agent、页面与主题动作跳转；
  子序列模糊过滤、↑↓/Enter/Esc、分组截断、完整 a11y（combobox/listbox/
  aria-activedescendant/焦点归还）。面板懒加载 + 空闲预取，快捷键注册在布局层
  （chunk 未到时也能响应），首屏体积不变。
- **阅读器窄屏修复**：<1024px 的「章节目录」「双语对照」入口原来点了没反应
  （桌面栏 hidden 但按钮仍可见），现在改为左右抽屉；桌面保持双栏。
- **阅读器稳定性**：
  - PDF 渲染任务在翻页/缩放前 cancel，取消异常不再被当成加载失败；
  - PDF worker 改为每次加载新建、cleanup 时 terminate（修掉"离开阅读器再进来
    报 PDFWorker.create - the worker is being destroyed"的真实缺陷）；
  - 引用 bbox 高亮只在 currentPage === citation.page 时渲染，翻页自动失效；
  - 目录高亮改为二分查找（原 O(n²)）。
- **可访问性**：canvas role=img + "PDF 第 N 页，共 M 页"标签；加载态 role=status；
  章节目录/参考文献/译文区使用 h2；硬编码 red/neutral 色值换成语义 token。
- **纯函数与测试**：新增 web/src/lib/reader.ts（parseBBox / bboxStyle /
  activeOutlineIndex / splitSentences）与 20 个边界单测；命令面板 6 个交互测试。
  前端测试从 32 个文件 / 214 个用例。

### 升级说明

- 无数据库/契约 schema 变更；契约 info.version 与 tag 同步到 6.5.0；
- 首屏 gzip 179,100 → 179,804 B（+0.39%），ReaderPage +0.69%，都在 bundle 预算内。

## [6.4.0] - 2026-11-14

安全与异常处理加固版本。默认开启两道新的防线，升级后**不需要改任何配置**，
但有几处客户端可见的行为变化（见「行为变更」）。

### 行为变更

- **上传内容嗅探**：扩展名对但内容不对的文件现在在上传阶段就返回 400
  `invalid_argument`，而不是存进去、处理失败、在文档列表里留一个 `failed`。
  PDF 必须以 `%PDF-` 开头；`.md/.markdown/.txt` 不能含 NUL 且必须是合法 UTF-8。
- **上传文件名净化**：客户端给的文件名会去掉路径部分与 ASCII 控制字符
  （`../../etc/passwd` → `passwd`）。依赖原始路径展示的用法会看到变化；
  落盘的 `storage_key` 一直是 UUID，不受影响。
- **SSE 空闲心跳**：服务端在一条流空闲 15 秒后补 `: keep-alive` 注释帧。
  注释帧是 SSE 规范的一部分，**客户端必须忽略以 `:` 开头的行**（我们的前端
  一直如此）。`lastEventId` 不因此推进，断线续传语义不变。
- **新增限流（默认开启）**：`/api/` 全局限流 240/min（burst 60），会调模型/
  embedding 的端点 30/min（burst 8）；超限返回 429 `rate_limited` + `Retry-After`。
  四个新环境变量见下；0 可关闭对应档位。
- **新增安全响应头**：`X-Content-Type-Options`、`X-Frame-Options: DENY`、
  `Referrer-Policy`、`Permissions-Policy`、`Cross-Origin-Opener-Policy` 与 CSP。
  自建前端若依赖被 CSP 拦截的能力（内联 script、跨域 connect、iframe 嵌入），
  需要在这条 CSP 下重新评估。
- **新增 4 个环境变量**（都可不配，用默认值）：

  | 变量 | 默认 | 说明 |
  | --- | ---: | --- |
  | `CONGORAG_RATE_LIMIT_PER_MINUTE` | 240 | 全局限流（令牌/分钟），0 关闭 |
  | `CONGORAG_RATE_LIMIT_BURST` | 60 | 全局桶容量 |
  | `CONGORAG_RATE_LIMIT_LLM_PER_MINUTE` | 30 | 模型端点限流，0 关闭 |
  | `CONGORAG_RATE_LIMIT_LLM_BURST` | 8 | 模型端点桶容量 |

  非法值（负数；开启档位但 burst 为 0）会让进程拒绝启动。

### 变更

- **安全响应头中间件**：`internal/platform/security.go`，排在 Recovery 之前，
  panic 产生的 500 也带这些头。
- **两档令牌桶限流**：`internal/platform/ratelimit.go`；按客户端地址分桶、
  懒清理，生产反代场景从 `X-Forwarded-For` 取真实来源（仅当 `RemoteAddr`
  是回环，且不盲信外部伪造的 XFF）。
- **上传校验**：`internal/knowledge/upload_validation.go`（扩展名/内容嗅探/
  文件名净化）；`Upload` 在落盘前完成校验。
- **SSE 心跳与写锁**：`apps/api/internal/api/sse.go`；心跳 goroutine 与
  Emit/兜底 error 帧/Flush 共用一把写锁，杜绝半截帧交错；handler 用
  `defer sink.Close()` 停心跳。
- **前端请求超时**：`packages/api-client` 的非流式请求加 120s 超时；
  流式请求不受影响；`fetchWithTimeout` 可单测。
- **文档**：`SECURITY.md` 扩成"内置防护 + 限流配置 + 已知取舍"三节；
  `docs/sse-protocol.md` 心跳章节改写；ADR-005 同步注记；`.env.example` 补变量。

### 升级说明

- 无数据库结构变更；契约 `info.version` 与 tag 同步到 6.4.0；
- 如果公网部署前面有反代：确认它透传 `X-Forwarded-For`（限流按真实来源分桶），
  并保持 `proxy_buffering off`（否则 SSE 的流式体感会被缓冲吃掉）。

## [6.3.0] - 2026-11-14

Prompt 审计与 Guardrails 版本：提示词从散落的字符串变成可审计资产。

### 行为变更

- **新增 `GET /api/v1/prompts`**：返回提示词注册表（每个条目的版本时间线、
  完整文本、SHA-256、变更原因、采样参数、代码位置）。只读，不提供写接口。
- **采样参数真正生效**：`WithTemperature` / `WithMaxTokens` 之前被 Eino 适配器
  丢弃（`_ = opts`）。升级后翻译等调用会按代码里写的温度执行（翻译 0.1、
  写作/对比/综述/图注/版本差异 0.3），输出风格可能比之前更稳定。这是本轮
  **唯一会改变模型输出的变更**。

### 变更

- **提示词注册表**：新增叶子包 `internal/prompt`（17 条提示词、版本化、
  SHA-256、变更原因、采样参数、可复现指标）；conversation / ctxmgr / llm /
  handler_research / handler_knowledge 的内联字面量全部改为通过它渲染；
  api 启动时 `prompt.Validate()` 校验注册表。设计见
  [ADR-010](docs/adr/010-prompt-registry.md)。
- **前端「Prompt 审计」页面**：侧栏新增入口，版本时间线与内容/哈希/代码位置
  可视化（只读）。
- **提示词审计文档**：`docs/prompt-audit.md` 增加注册表指针，并修正
  language temperature=0.1 曾未生效的记录。
- **启动装配**：api 启动时校验提示词注册表，失败拒绝启动（与数据库连不上同一条原则）。

### 升级说明

- 无数据库结构变更、无新增环境变量；
- 契约 `info.version` 与 tag 同步到 6.3.0（ADR-002）；
- 唯一行为差异是采样参数生效（见上）；如果某个模型的输出因此变化，可在
  `internal/prompt` 对应版本里改 `Sampling` 并同步文档。

## [6.2.0] - 2026-11-14

架构解耦与性能基线版本。**无运行时行为变更**，升级不需要任何额外操作。

### 变更

- **HTTP 层按资源域拆分**：`apps/api/internal/api/server.go` 从 2049 行拆成
  `server.go`（外壳与公共工具）+ `convert.go`（DTO 转换）+ 六个
  `handler_*.go`（知识库/文档、科研助手、arXiv、BYOK 与用量、会话与 SSE、Agent）。
  决策与验收方式见 [ADR-009](docs/adr/009-api-handler-layout.md)。
- **切词规则去重**：`knowledge` 包里那份与 `tokenize` 重复的切分实现删除，
  统一走 `tokenize.Tokens`（保留词频，供 SimHash 使用）；`tokenize.Text`
  在它之上做去重。检索与指纹从此共用同一套规则。
- **近重复去重性能改造**：`retrieval.dedupeNearDuplicates` 的 5-gram 集合表示
  从字符串换成排序去重的 64 位哈希切片。同机同语料：`203,333 → 161,000 ns/op`、
  `186,564 → 48,664 B/op`、`3,319 → 22 allocs/op`。旧实现保留在测试里作为对照。
- **纯函数基准与性能文档**：新增 `make bench` / `make bench-dedupe`，覆盖
  tokenize、grounding、retrieval、knowledge、platform、visualize 六个包共 12 组
  生产基准 + 2 组对照基准（大 topK 场景与旧实现对跑）；基线与测法写进
  [docs/performance.md](docs/performance.md)。
- **前端请求层契约化**：11 + 4 处手写 `fetch` 改走 `@congorag/api-client`，
  统一错误解包（RFC7807 的 `type` 重新可见，错误提示回到中文文案而不是原始 JSON）；
  新增 `scripts/bundle-report.mjs` 与 `docs/perf/bundle-baseline.json`，
  首屏体积可按基线比较。
- **质量基线**：新增 [docs/quality-baseline.md](docs/quality-baseline.md)，
  记录本机可验证的绿灯基线、覆盖率与"只能 CI 验证"的项目清单。

### 升级说明

- 无数据库结构变更、无契约端点变化、无新增环境变量；
- 契约 `info.version` 与 tag 同步升到 6.2.0（见 ADR-002）；
- `make bench` 是新增的开发命令，不影响任何运行时路径。

## [6.0.0] - 2026-11-13

单一主线合并版本。

### 变更

- **合并项目与比赛版本**：删除 `cup-2026` 分支与 `competition-base-v5.0.2` 标签；
  `docs/competition/` 拆分为 `docs/modules.md`、`docs/submission/`、
  `docs/evaluation/` 与 `docs/prompt-audit.md`；`deployments/competition` 改为
  `deployments/production`。
- **README 改为产品主线**：移除"基线/参赛增量"叙事，当前 `main` 即提交版本与
  后续维护主线；版本号统一为 6.0.0。
- 新增/完善科研助手能力：上传语义去重与版本差异、首页元信息块、批量上传、
  双语滚动同步/悬停翻译、arXiv 推荐与自动追踪、PageRank 图谱与自动综述、
  完整论文框架 + Graphviz、CSV 可视化 Matplotlib 代码。
- 清理旧的参赛版视频脚本与过期目录，提交材料统一到 `docs/submission/`。

### 升级说明

- 无数据库结构变更（自 5.0.2 以来的科研助手迁移 0015–0019 已在旧版本中包含）；
- 仓库路径变化：旧链接 `docs/competition/...`、`deployments/competition` 不再存在。

## [6.1.0] - 2026-11-13

网站与品牌统一版本。

### 变更

- **项目名统一为 CongoRAG**：应用侧栏、页面标题、favicon、公开首页、README、技术文档与提交材料
  中的 `ConGoRAG` 全部改为 `CongoRAG`。
- **公开首页重做**：改为与演示应用同一套深色语言：真实界面截图做 Figure，滚动分节讲解析、证据、图谱、写作；
  顶部与首屏的演示入口直接跳转，不再经过底部重复 CTA；移动端改成独立的单列顺序。
- **应用壳层**：侧栏品牌两行（CongoRAG / 科研助手）、当前页标题、移动端品牌、跳到主要内容、
  每页动态 document.title；知识库卡片收紧高度、换文件图标、支持两行标题、补 focus 样式。
- **性能与可访问性**：首页无外部字体/JS 依赖，首图 eager、其余 lazy + 宽高；静态资源走
  `/landing/` 缓存；补 canonical、OG、JSON-LD、theme-color；正文最小 13px，修正对比度。
- 深色首页去掉评审账号、技术文档清单和底部重复说明；应用顶栏去掉品牌面包屑与口号，
  只保留当前页标题。

### 升级说明

- 无数据库结构变更；
- 首页静态资源路径新增 `/landing/`，生产 nginx 已配置公开缓存。

## [Unreleased]

### 变更

- **日报工具拆分为独立项目 DSP**：`tools/daily` 及 `congorag-daily` 二进制的
  构建与部署配置一并从本仓库移除（`site-release.yml` 只发布站点产物，
  `ops/deploy-site.sh` 只部署站点）。日报此后由独立项目 DSP 维护。

## [5.0.2] - 2026-09-22

v5.0.1 修掉了集成测试的失败，CI 于是跑到了下一步，又暴露出第二个问题。
**这一版同样只影响测试与 CI，不影响任何运行时行为** —— 已经发布的
v5.0 / v5.0.1 产物都无需替换。

### 修复

- **CI 的契约测试会红**：`scripts/contract-test.mjs` 的检索那条断言期待 200，
  但它跑在集成测试之后、用的是**同一个库** —— 而 `internal/llm` 那批集成测试
  会模拟"从没做过 BYOK 的全新数据库"，把 embedding 模型配置清掉。于是 api
  返回 404（`no embedding model configured yet`），而**契约对这条请求同样
  声明了 404**。这个矛盾一直存在，只是前几次 CI 都在上一步就停了、这一步
  从没跑到过。
  修法是让那条断言按契约放行两种结局（200 时仍校验 `hits` 是数组），
  并给 `call` 的 `expect` 加上多值支持

## [5.0.1] - 2026-09-22

v5.0 的集成测试在 CI 上会失败，**只影响测试，不影响任何运行时行为** ——
已经发布的 v5.0 产物无需替换。

### 修复

- **CI 的 Integration job 会红**：`internal/llm` 的集成测试会把
  `document_chunks.embedding` ALTER 成 `halfvec(768)`（那个文件自己记着这条
  「会动全局 schema」的副作用），而 v5.0 新增的 `internal/retrieval` 集成测试
  用的是 1 维的假 embedder。两者共享同一个库时（CI 就是一个容器跑
  `go test ./...`）必然撞上 `different halfvec dimensions 1 and 768`。
  本地 `make test-integration` 看不出来 —— 那条路每个包起自己的容器。
  修法是让那几条测试**自己**把向量列设成它要用的维度，用完恢复，不再依赖
  别的包留下的状态

## [5.0] - 2026-09-22

这一版**没有新功能**：它是对 v4.0 落地之后的一次全面审查与修复。五路并行审查
（域逻辑 / API 层 / 数据库 / 前端 / 工程化）产出 34 条（#97–#130），全部落地，
改的都是**已经交付的行为里那些不正确的、会静默出错的部分**。

### 行为变更

| 变更 | 说明 |
| --- | --- |
| **升级前必须先跑 `make migrate-up`** | 新增三条迁移：`0012_token_usage_indexes`（补上 `token_usage` 两个外键列的索引 —— 级联删会话或删模型时不再全表扫描这张唯一会无限增长的表）、`0013_enum_constraints`（七处文本枚举列补 `CHECK`，并删掉一条被唯一约束完全覆盖的冗余索引）、`0014_retention_indexes`（事件表剪枝用的索引）。漏跑不会立刻出错，但用量表会随使用线性变慢 |
| **`CONGORAG_PORT` 环境变量被移除** | 它此前**从未生效**（`Config.Port` 全仓零读取点），真正决定监听地址的一直是 `CONGORAG_LISTEN_ADDR`。README 的配置表里也删掉了它 —— 留着会让人以为改了它就能换端口。compose 里那个同名变量是另一回事（宿主机端口映射），不受影响 |
| **聊天与 Agent 输入有了长度上限** | `SendMessageRequest.text` 与 `StartAgentRunRequest.input` 在契约里补了 `maxLength: 8000`。超限返回 `invalid_argument`，且**不写任何消息行**（此前会留下一条永远没有回答、也永远不会被清理的占位气泡）。直连 API 的调用方按严格 schema 解析时需要跟着改 |
| **JSON 端点有了 1 MiB 请求体上限** | 此前只有文档上传有上限。超限返回 400、文案里带上限值。上传仍走 `CONGORAG_MAX_UPLOAD_BYTES`，不受影响 |
| **服务端新增 5 分钟 `ReadTimeout`** | 此前只设了 `ReadHeaderTimeout`。对 SSE 安全 —— 读超时只覆盖读请求，而 SSE 的长处在响应方向（这一点是实测确认的，不是照抄结论） |
| **SSE 的错误帧不再泄漏 5xx 内部错误原文** | 此前工具失败会把 SQLSTATE、表名、连接串一路发到前端 toast 的第二行；而"5xx 不返回原文"这条策略此前只在兜底帧成立，兜底帧又只在主帧发不出去时才跑 —— 所以那条测试是假绿。现在两条路径走同一个判据 |
| **SSE 的 `id: 0` 不再出现在线路上** | "运行根本没开始"那类错误帧（空输入 / agent 不存在 / 没有可用模型）此前会带 `id: 0`。按协议，不带 `id` 的帧才不更新续传游标；写 0 会把游标退回起点、下次续传重放整轮。协议文档一直是对的，是实现在违反它 |
| **SSE 的错误类型补齐了三档** | `conflict` / `conflict_duplicate_key` / `not_found` 此前在 SSE 侧一律落成 `internal_error`，而 REST 侧是分开的 —— 同一类失败在两条通道上 type 不同。现在由同一张表产出 |
| **工具结果截断到 16 KiB** | 此前工具结果原样进模型消息历史，一个检索工具就能把上下文撑爆；而撑爆后上游返回的错误会被归类成 `upstream_llm_error`，把用户引向"检查 API Key 和配额"—— 排查方向从第一句话起就是错的。超限结果现在截断，并**显式告诉模型"结果被截断了"** |
| **事件表的保留窗口是 24 小时** | `conversation_events` / `run_events` 现在按时间剪枝（此前只增不减 —— 实测 6 个会话攒了 4853 行，其中 97.5% 是流式 token 事件）。**代价**：超过 24 小时的 run 走 run 级重订阅只会拿到空历史（`GET /runs/{id}` 的 input/output/steps 不受影响）。`tool_effect_log` **不按年龄剪** —— 它是"工具是否已执行过"的唯一判据，剪早了会让恢复重放已执行过的工具 |
| **流式 token 事件改为攒批落库** | 此前每个 chunk 写一行库（两个往返）。现在按 300ms / 1 KiB 合成一条。客户端看到的帧类型与语义不变 |

### 修复

（以下都不改变 API 形状，列出来是为了让"这一版到底改了什么"可查。）

- 文档索引把 embedding 的 HTTP 调用包在写事务里 —— 一条连接被占住最长 30 分钟，10 个并发文档任务就能占满连接池、把同队列的维护任务一起饿死
- 取消 / 断线时正在执行的那一步工具会**永远停在 `running`**，而 ADR-007 崩溃表的第一判据就是"有没有一行 running 的 step"
- 工具效果账本用会被取消的请求 ctx 写、失败只记一行日志 —— 会让"工具到底跑没跑过"在最需要它的那一刻失准
- `Resume` 重放失败的收尾用请求 ctx 且丢弃错误，run 会卡在 `running` 且既不可恢复、界面也不会显示失败
- 模型配置不校验 `contextWindow > maxOutputTokens`，两个数字填反后每条消息都失败、且没有任何线索指向引导页
- 文档列表的分块数聚合抹掉了 keyset 分页的索引优势 —— 每翻一页都要扫全库文档再排序
- 两个周期任务对每个会话单独发查询（N+1），且用不上 `0011` 新建的 `updated_at` 索引
- 幂等补发每 100ms 轮询一次事件表、无退避，最长持续 10 分钟
- `Bootstrap` 在请求路径的事务里做表重写 + 非 `CONCURRENTLY` 建 HNSW 索引，全程 ACCESS EXCLUSIVE
- SSE 已经开始后 handler panic，Recovery 会把一段 JSON 写进 `text/event-stream`，客户端表现为流静默结束
- worker 只配了一个队列，一次批量上传就能把周期维护任务饿死数小时
- **前端**：乐观上屏的消息被插进"已加载页里最旧"的那一页 —— 翻过历史后提问会跑到记录中间，表现为"提问看不见、回答在跑"
- **前端**：离开页面或切换会话不中止 SSE —— 额度继续烧，而那个位置上的取消入口已经跟着页面消失
- **前端**：`conversations/:id` 没有按 id 加 key，会话间前进/后退时流式状态会串到另一个会话上
- **前端**：流式过程中每个 token 都给每张工具卡片做一次全量 `JSON.stringify`（截断发生在序列化之后）
- **前端**：发送之后输入框失焦，键盘用户每一轮都要重新点一次
- **前端**：语法高亮把 36 种语言全打进会话页（单个 chunk 334 KB，其余页面 2–20 KB）→ 改为 16 种语言按需加载，会话页降到 228 KB
- `make release-dry` 承诺"不碰 git、不联网"却仍在顶层发一次 `git ls-remote`
- `upgrade.mjs` / `rollback.mjs` 零自动化 —— 它们会 `DROP DATABASE` + 清卷，是全项目代价最高的一条路径，却一个门禁都没有
- CI 五个 job 没有一个碰 Docker（Dockerfile、两份 compose、`migrate-entrypoint.sh` 全无覆盖），改坏了要等到打 tag 才红
- `evals/measure_recall.sh` 的「N/A（分块数为 0）」分支永远不可达 —— `grep` 无匹配返回 1，配上 `set -e` 让脚本半路无输出地死掉
- SPA 路径穿越的回归测试是自证的：假文件系统里没有哨兵文件，断言在任何实现下都成立
- 若干文档与注释漂移（`agents/runs/{runId}/events` 这个不存在的旧路径、ADR 引用的迁移文件名、SSE 心跳、README 里 test-integration 的描述停在两版之前）

## [4.0] - 2026-09-22

这一版把 v3.0 之后那批 issue（#54–#96）做完：Agent 的运行控制与崩溃恢复、
检索调试视图、一批前端能力与无障碍，以及发布/交付/升级那条工程链。

### 行为变更

| 变更 | 说明 |
| --- | --- |
| **升级前必须先跑 `make migrate-up`** | 新增三条迁移：`0009_run_events`（run 维度的事件表与计数器）、`0010_tool_effect_log`（工具副作用账本）、`0011_conversation_activity`（会话的最近活动时间回填 + 索引）。漏跑的话，跑一次 Agent 会在写事件时失败 |
| **Agent 的运行流新增首帧 `run_started`** | `POST /api/v1/agents/{id}/runs` 与 `POST /api/v1/runs/{runId}/resume` 的响应流，**第一帧永远是** `event: run_started`、`data: {"runId": "..."}`，新建与幂等重放都会发。此前五种事件里没有任何一种带 run id，客户端因此拿不到"这次运行叫什么"，也就没法调取消端点。**解析器要能容忍这个新的帧类型**（照 `docs/sse-protocol.md` 的帧格式读即可，未知 `event` 类型跳过） |
| **`POST /api/v1/agents/{id}/runs` 接受 `Idempotency-Key`** | 命中同一个键时不重新执行，而是补发那条 run 已记录的事件（首帧同样是 `run_started`，带的是原来那条 run 的 id）。保留窗口与发消息那条路径共用 24 小时（ADR-008） |
| **`Document` 的响应多了一个**必填字段 `chunkCount` | 该文档当前的向量分块数，未处理完的文档是 `0`（不是 null）。**直连 API 的调用方**如果按严格 schema 解析，需要跟着改 |
| **会话的 `updatedAt` 语义变了** | 它现在真的是"最近活动时间"（每次收到消息就更新），会话列表 `GET /api/v1/conversations` 按它倒序。在此之前它只在创建那一刻写过一次——**所以它在旧数据上等于 `createdAt`**，迁移 `0011` 会把已有会话回填成"最后一条消息的时间" |
| **`GET /api/v1/runs/{runId}/events` 与 `POST .../cancel`、`.../resume`** 见下面的"新增" | run 维度的断线重订阅、取消与断点恢复都在这一版。路径在 `/runs/{runId}/` 下，不是 `/agents/runs/{runId}/` —— runId 全局唯一，放在 `/agents/{id}` 那层会与路径参数混用静态段（见契约里那段注释） |
| **Agent 的 `interrupted` 状态现在真的会被写入** | 两个来源：进程**启动时**的扫描（把上个进程留下的 `running` 运行标成它，见 ADR-007）与客户端中途断开。**只有 `interrupted` 的运行可以恢复**；`completed` / `failed` / `cancelled` 都是终态 |
| **错误类型新增四个** | `state_schema_version_mismatch`、`tool_effect_already_applied`、`replay_unsafe`（恢复端点，都是 409），以及 Agent 流上的同名 `error` 帧 type。前端按 `type` 分支的映射表要跟着补 |
| **上游模型服务出错时的归因更准** | 工具自己失败（查不到那一行之类）不再被说成 `upstream_llm_error`——那是前一版就修的方向，这一版把恢复相关的几种也补齐了 |

### 新增

- **端点**：`GET /api/v1/conversations`（按最近活动时间倒序、keyset 分页）、
  `DELETE /api/v1/conversations/{id}`、`POST /api/v1/knowledge-bases/{id}/search`
  （检索调试：直接返回命中的分块与相似度）、`GET /api/v1/runs/{runId}/events`、
  `POST /api/v1/runs/{runId}/cancel`、`POST /api/v1/runs/{runId}/resume`、
  `PATCH /api/v1/agents/{id}`、`PATCH /api/v1/models/{id}`、`DELETE /api/v1/models/{id}`
- **SSE 事件类型** `run_started`（见上）
- **崩溃恢复**：Step 边界的 checkpoint、按 ToolMetadata 决定能不能重放、
  效果账本（`tool_effect_log`）判读"工具是否被重复执行"，以及终态运行的
  checkpoint 回收（worker 每小时一次）
- **取消**：运行可以中断，终态是 `cancelled` 而不是 `failed`
- **交付**：`.github/workflows/release.yml`（六平台产物）、启动包
  （compose + `.env.example` + 镜像 tarball 的 zip）、升级与回滚脚本，
  以及用户视角的 [`docs/upgrading.md`](docs/upgrading.md)
- **工程**：契约 lint（`make lint-spec`）、契约测试（`make test-contract`，
  Prism proxy 模式）、集成测试改用 testcontainers（`make test-integration`
  自己起容器，不再要求开发机上有 PostgreSQL 在跑）、崩溃探针
  （`make crash-probe`）

### 修复

- `POST /agents/{id}/runs` 的流里此前**不带 run id**，取消端点因此无从下手
- `StartAgentRun` 缺兜底 error 帧：数据库不可用时客户端会拿到 200 + 空 body，
  与"空的成功流"完全同形（发消息那条路径早就修过同一个问题）
- `conversations.updated_at` 一直等于 `createdAt`——接口暴露的 `updatedAt`
  因此一直在说谎

## [3.0] - 2026-09-21

这一版是**能力建设**，不是缺陷修复：v2.0 修完了 v1.0 的 36 个缺陷，v3.0 把
那些"文档里承诺了、代码里没有"的功能补齐，并还掉一批工程债。

### 行为变更

| 变更 | 说明 |
| --- | --- |
| **换 embedding 模型不再一律被拒** | v2.0 起换模型返回 409，且应用内没有恢复入口，只能手工清库。现在第一次提交仍返回 409，但 `type` 是 `embedding_change_requires_reindex`，前端据此弹确认框；用户确认后带 `allowEmbeddingReset: true` 重发，服务端在**同一个事务里**清空旧向量、改列类型、把全部文档重新排队重建 |
| **未声明工具能力的模型会让带工具的 Agent 不可用** | Agent 的创建与运行现在会读 `Capabilities.ToolCalling`。为了不"升级即坏"，引导页的复选框**默认改成勾选**，并有一条迁移把已有的 chat 模型行回填上这一位——所以**对已有配置是零变化**。但如果你在引导页把它取消勾选，带工具的 Agent 会立刻不能创建/运行 |
| **三个列表端点改成游标分页** | `GET /conversations/{id}/messages`、`GET /knowledge-bases/{id}/documents`、`GET /agents/{id}/runs` 的响应从裸数组变成 `{items, nextCursor}`，并新增 `limit` / `cursor` 查询参数。**这是破坏性变更**，直连 API 的调用方必须改 |
| **会话消息的第一页是最新的 50 条** | 不是最旧的。翻下一页拿更早的（聊天页打开就该看到最近发生的事） |
| **`/healthz` 现在只报进程存活** | 新增 `GET /readyz` 探数据库。交付期做健康检查时：**重启策略看 `/healthz`，流量门禁看 `/readyz`**——反过来会让数据库抖动变成一次实例重启 |
| **首次启动需要能访问一次 `openaipublic.blob.core.windows.net`** | tiktoken 词表现在在启动时预热，失败就**拒绝启动**（而不是起来之后每条消息都报错）。缓存落在 `CONGORAG_TIKTOKEN_CACHE_DIR`（默认 `./data/tiktoken-cache`），只需要成功下载一次；离线环境的报错里带着缓存目录、下载地址和文件名怎么算 |
| **新增三条迁移** | `0006_idempotency_replay`（幂等键）、`0007_agent_tool_calling`（能力位回填）、`0008_list_pagination_indexes`（分页索引）。**升级前必须先跑 `make migrate-up`**——漏跑 0006 的直接后果是聊天整条路径 500 |
| **token 用量开始记账** | 每次模型调用写一行 `token_usage`（此前这张表没有任何写入者）。行数 = 调用次数，所以本地库会开始增长 |

### Added

- **幂等键重放**：`POST /conversations/{id}/messages` 接受可选的
  `Idempotency-Key` 请求头。同一个会话带同一个键重复提交时，服务端**不重新
  生成**，而是把那一轮已记录的事件补发一遍（同样的帧类型、同样的真实
  `event_id`）。键保留 24 小时；同一个键配不同的正文会返回
  `invalid_argument` 的错误帧，而不是静默重放旧答案
- **重新索引**：`POST /documents/{id}/reindex` 与
  `POST /knowledge-bases/{id}/reindex`。换模型之后重建、或者只重跑某一份处理
  失败的文档
- **就绪探针** `GET /readyz`：数据库不可用时返回 503 +
  `application/problem+json`
- **用量读端点** `GET /api/v1/usage`：按模型聚合的 token 用量，带
  `since` / `until` 时间窗
- **优雅退出**：两个进程都接上信号。api 会先拒新请求、再取消在途请求的 ctx
  排空 SSE（在途请求最长等 30 秒，超时强制关闭连接）；worker 给正在跑的任务
  留 60 秒收尾，超时后强取消
- **静态资源缓存头**：`assets/` 下的内容哈希产物是
  `public, max-age=31536000, immutable`，`index.html` 与 `favicon.svg` 是
  `no-cache`，错误响应是 `no-store`
- **前端路由级拆包**：6 个页面改成 `React.lazy`，并新增
  `RouteErrorBoundary`（chunk 加载失败时给一个"刷新页面"的按钮，而不是白屏）
- **按钮级错误提示（toast）**：写操作失败改为 toast；查询失败与流式生成失败
  **仍然保留页内 Alert**（前者失败后页面本来就没内容，后者是"这一轮失败了"的
  持久记录）
- **`make test-integration`**：本地一键跑集成测试。它在**每次重建的独立测试库**
  （`congorag_test`）上跑，不碰开发库
- **eval 数据集**：`evals/` 下有语料、20 道题（14 道能答 + 6 道不该答）与
  指标脚本。它测的是"检索到的内容对不对"，与原有的 `measure_recall.sh`
  （测 HNSW 近似索引的误差）分工不同
- **文档**：`docs/adr/` 从 1 篇补到 5 篇；新增 `docs/testing.md`
  （平台相关行为怎么测）与 `docs/releasing.md`（发布流程）

### Changed

- **`Capabilities.ToolCalling` 的契约默认值改成 `true`**，引导页复选框默认
  勾选（见上面的行为变更）
- `GET /conversations/{id}/events` 的契约描述改为事实：**它补发完历史就结束
  响应**，不持有连接等新事件（那条承诺从来没有被实现）
- `docs/sse-protocol.md` 里"命中幂等键后用 `after_event_id=0` 重新订阅"那段
  是错的，已改写：`event_id` 按会话发号，`0` 会把此前每一轮的 token 全部重放
- `contracts/openapi.yaml` 的 `info.version` 从 `0.1.0` 改成 `2.0`，从此
  跟产品版本走（见 [ADR-002](docs/adr/002-contract-versioning.md)）。
  **兼容性承诺的边界仍然是路径里的 `/api/v1`，不是这个号**

### Fixed

- **`useTheme` 的多个消费者会失同步**（每个实例各存一份主题状态）——侧栏切
  主题时其它消费者永远停在旧值。改成共享 store
- **`Ready` 状态的文档落进 `ProcessDocument` 的 default 分支报"状态冲突"**
  ——重新索引的安全网现在接住它
- **`TestIntegration_Bootstrap_EndToEnd` 的向量验证会在空库上跳过**——它原本
  依赖"环境里恰好有一个知识库"，改成自己造探针数据
- 对话页加载更早一页时会把视口强行拉到底部（滚动的依赖从整个列表改成最后
  一条消息的 id）

### 未实现（有意为之）

- **`memories` 的重嵌入交给周期任务**，不是切换事务的一部分：清空在那个事务
  里完成，重建由 worker 的下一次 tick 自愈。所以换模型后长期记忆会有一段时间
  检索不到
- **`messages.token_usage` 这一列继续留空**：`token_usage` 表是唯一真相，
  两处同时维护必然漂移。要按消息显示用量时用 `where message_id = ...` 查
- **`GET /api/v1/usage` 只做按模型聚合**，不做按会话聚合——后者要 JOIN
  `messages`，而只有聊天主路径有 `message_id`，按会话加起来会天然少于总量
- **契约版本仍不表示兼容性**：见 ADR-002 的"必须一起记住的"

### 已知限制

- **本机跑不了 `go test -race`**（没有 gcc 且 `CGO_ENABLED=0`），竞态检测只有
  CI 的 ubuntu runner 会跑。这是本地验证与 CI 的唯一差异
- **平台相关行为的测试证据**：临时文件清理顺序那条修复现在有一条与平台无关
  的测试（`closeAndRemoveTempFile` 的接缝），但"Windows 上句柄未关时删不掉"
  这个事实本身仍然只在 Windows 上可复现。见 `docs/testing.md`
- **完整 eval 不进 CI**：它要打真实的 embedding / chat API、花钱、依赖网络，
  且回答生成没有固定 temperature 所以结果随机——放进 CI 会得到一条随机翻红的
  流水线。只有指标数学的单测进 CI
- **服务端主动关停时，在途 SSE 收到的错误类型不够诚实**：优雅退出（#40）会让在途的流以一条
  `type: internal_error`、detail 为 `client disconnected: conflict` 的错误帧收场——原因是
  关停复用了"客户端断开"那条路径。对本地单机应用影响很小（关停时用户本来就在重启服务），
  要修得新增一个 `service_unavailable` 错误类型并同步改 `sseerr` / `problem.go` / 前端文案表，
  **那是一次影响所有错误路径的改动，不放进这一版**
- **本次提交与 tag 未签名**（本机没有可用的 GPG 私钥）

## [2.0] - 2026-09-21

v1.0 全量代码经过一轮 15 个维度、带对抗性验证的审查，发现的 36 个缺陷全部
修复。每个缺陷有独立 issue（都在
[v2.0 里程碑](https://github.com/XiaoleC05/CongoRAG/milestone/1)），每个修复
带一条回归测试。

### 行为变更

| 变更 | 说明 |
| --- | --- |
| **换 embedding 模型会被拒绝（409）** | 之前重复引导会静默清空全部向量，文档仍显示 `ready`、检索静默返回零条。现在：列类型已经是目标值就跳过 `ALTER`；已存向量不属于当前模型时返回 409。**代价：换模型需要手工清库**——当时没有重嵌入路径，应用内也没有清空向量的入口（3.0 补齐了） |
| **上传有 32 MiB 上限** | `CONGORAG_MAX_UPLOAD_BYTES` 可覆盖；写 0 或负数视为没设、退回默认值 |
| **文档处理任务超时 30 分钟** | 之前吃 River 的 1 分钟默认值，一份合法的大文档必然超时。与上传上限相互作用，两条要一起看 |
| 失败语义 | 非最后一次 attempt 不落终态；终态重投返回 nil；客户端断开记 `interrupted` 而不是 `failed` |
| SSE 的 error 帧可以不带 `id` | 唯一那一帧由"事件没能落库"触发，**不要拿它推进续传游标**（已写进协议文档） |

### Added

- 前端测试基础设施（vitest + jsdom + Testing Library），并让 CI 真的跑它们
- 上传的扩展名与 UTF-8 编码校验
- 文档处理失败时的日志与孤儿文件清扫
- README 与 GitHub Actions CI（提交 `5101cf8`、`b6ec324`）——这两个提交落在
  v1.0..v2.0 窗口内，但当时的发布说明没有提到它们

### Fixed

**对话与上下文**

- 历史按最新在前交给模型（`RecentMessages` 倒序返回，调用链上没有一层重排）：
  现在查询内层取最近 N 条、外层按时间序返回
- 本轮自己刚写入的消息被当作历史回放：当前提问不再被重复计入预算，空的
  assistant 占位行不再进 prompt
- 回放历史的角色被压平成 `user`：`ctxmgr.Item` 补上 `Role` 并一路传下去，
  助手自己说过的话不再记在用户头上
- 检索片段与记忆被拼进 system 消息：改为单独的 user 角色消息 + 定界符，
  并剥离正文里自带的定界符
- 摘要水位线跨过仍在流式生成的行：批次截到最后一条已定稿的消息
- 空 completion 覆盖已有摘要：空值判定后拒绝写入
- 压缩器忽略 `targetTokens`：预算写进提示词，压缩结果不优于被替换区域时整条丢弃

**流式与失败语义**

- 客户端断开时清空已生成的部分回答：正文保留，只改状态
- 数据库故障时 error 帧发不出去、客户端拿到 200 空响应：补一条不依赖落库的
  兜底帧

**文档处理**

- 失败时用已取消的 job ctx 写状态且丢弃错误 → 用脱离取消的 ctx，并补记日志
- 终态 `failed` 与 River 重试自相矛盾（重试 25 次全撞在"状态冲突"）→ 只有
  最后一次 attempt 落终态
- 整篇文档塞进一次 embedding 请求 → 按 256 分批并校验每批数量
- 临时文件清理在句柄未关闭时执行（Windows 上是空操作）→ 先关闭再删除
- 上传无大小上限、文档处理继承 River 1 分钟默认超时 → 见「行为变更」

**BYOK 与密钥**

- 重复引导会执行 `ALTER ... USING NULL` 清空全部已存向量 → 见「行为变更」
- 集成测试自身的清理也清空向量、且断言发现不了 → 改为断言向量存活
- `tokenizer_type` 保存时不校验、聊天时才拒绝 → 保存时校验并 trim 落库
- 主密钥首次生成非原子（api 与 worker 同时启动各装一把不同的密钥，败者加密的
  数据永久无法解密）→ 临时文件 + fsync + `os.Link` 抢占 + 回读校验

**Agent**

- 客户端断开后 run 永久停在 `running` → 失败收尾用脱离请求的 ctx；补上
  `interrupted` 终态
- 无缓冲通道导致 goroutine 与 Eino iterator 每次中断泄漏一个 → 发送可取消
- 工具错误（如除零）打死整轮 run → 参数级错误降级为 tool result 交回模型
- 省略 `toolNames` 时 nil 绑定进 `NOT NULL` 列返回 500 → 归一化为空切片
- 一轮里多个并行工具调用写出多条 `llm` 步骤 → 同一轮只记一次
- 失败一律报 `upstream_llm_error` → 保留错误自带的 sentinel

**检索与平台**

- HNSW 后置过滤导致 `Search` 返回少于 `topK`（甚至零条）→ 在查询内设置
  pgvector 扫描参数
- 周期维护任务继承 60s 默认超时、被截断的一轮仍记为成功 → 显式超时并如实上报
- `isLoopbackHost` 无条件按末位冒号截断，无端口 IPv6 回环被判 403 → 改用
  `net.SplitHostPort`

**前端**

- 保存 provider 成功后仍被弹回空白引导页 → 只在"结论还是空"时等待
- 发消息时用户那条从不进消息缓存，失败时从界面静默消失 → 发送那一刻乐观插入
- Agent 运行失败后历史列表不刷新 → 缓存失效从 `done` 分支挪进 `finally`

**文档与契约**

- README / `docs/sse-protocol.md` 声称支持 `Idempotency-Key` 重放，实际没有
  代码读这个头 → 文档改为描述真实行为
- `openapi.yaml` 缺 `uploadDocument` / `deleteDocument` / `createConversation`
  会返回的 404 → 补齐
- `Capabilities.ToolCalling` 注释声明门控 Agent 可用性，全仓没有一处读它 →
  注释改为说清现状
- `make build-web` 删除被跟踪的 `apps/api/web/.gitkeep`（提交后会让新 clone
  编译失败）→ 构建后补回占位文件
- Makefile 的 `PATH` 用分号拼接，只在 cmd.exe 下成立 → 按平台选分隔符

### 未实现（有意为之）

以下三条本来是"文档与代码不一致"，按各自的修复方向让两边一致，**没有顺手
实现功能**（3.0 补齐了前两条与第三条的一部分）：

- `Idempotency-Key` 重放（SSE 重放语义不简单，属于新功能）
- `Capabilities.ToolCalling` 门控（表单默认不勾选，真加门控会让默认配置的
  用户失去 Agent）
- 重嵌入 / reindex 路径

## [1.0] - 2026-09-20

**首个发布。** 169 个文件、30722 行插入。

> 这一节依 tag 消息、提交 `362cab0` 与 README 重建——v1.0 当时只有 tag，
> 没有对应的 GitHub Release 对象。

### Added

- **知识库管理**：列表 / 详情 / 新建 / 改名 / 删除
- **文档上传**：multipart → 落盘（写临时文件 → fsync → rename）→ 入队 →
  解析 / 切分 / 向量化，处理状态可查
- **向量检索**：pgvector HNSW，引导时按 embedding 维度建索引，按知识库限定，
  返回相似度与文件名
- **流式问答**：SSE 逐 token 下发，citation 随流推送，按 `after_event_id`
  断线续传，协议见 `docs/sse-protocol.md`
- **上下文管理**：按 token 预算组装上下文，超预算的历史交给模型压缩
  （真实 tokenizer + 摘要 + 长期记忆）
- **Agent**：Eino ADK，工具注册表（计算器 / 知识库检索 / 会话检索），
  运行轨迹逐步落库
- **BYOK**：provider / model 配置存库，API Key 用 AES-GCM 加密后落盘，
  引导页采集
- **worker**：River 消费端（文档处理 + 孤儿文件对账）
- **单体交付**：前端产物 `go:embed` 进 Go 二进制
- 契约 `contracts/openapi.yaml` 与两端的生成物
- 两套迁移系统（业务表用 golang-migrate、River 队列表用它的 CLI）
- `evals/measure_recall.sh`：测 HNSW 近似索引相对精确扫描的召回差异

---

[Unreleased]: https://github.com/XiaoleC05/CongoRAG/compare/v6.7.1...HEAD
[6.7.1]: https://github.com/XiaoleC05/CongoRAG/compare/v6.7.0...v6.7.1
[6.7.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.6.0...v6.7.0
[6.6.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.5.0...v6.6.0
[6.5.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.4.0...v6.5.0
[6.4.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.3.0...v6.4.0
[6.3.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.2.0...v6.3.0
[6.2.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.1.0...v6.2.0
[6.1.0]: https://github.com/XiaoleC05/CongoRAG/compare/v6.0.0...v6.1.0
[6.0.0]: https://github.com/XiaoleC05/CongoRAG/compare/v5.0.2...v6.0.0
[5.0.2]: https://github.com/XiaoleC05/CongoRAG/compare/v5.0.1...v5.0.2
[5.0.1]: https://github.com/XiaoleC05/CongoRAG/compare/v5.0...v5.0.1
[5.0]: https://github.com/XiaoleC05/CongoRAG/compare/v3.0...v5.0
[3.0]: https://github.com/XiaoleC05/CongoRAG/compare/v2.0...v3.0
[2.0]: https://github.com/XiaoleC05/CongoRAG/compare/v1.0...v2.0
[1.0]: https://github.com/XiaoleC05/CongoRAG/releases/tag/v1.0
