# 六轮迭代记录（v6.2.0 → v6.7.0）

> 这份文档记录 2026-11-14 的六轮迭代：每轮都按
> **评判审查 → 子智能体讨论 → 执行落地 → 同步 GitHub 与文档** 的闭环推进。
> 每轮一个 minor 版本、一个 tag、一次可回滚的提交；所有数字都可用
> `docs/quality-baseline.md` 与 `CHANGELOG.md` 复核。

| 轮次 | 主题 | 版本 | 主要交付 |
| --- | --- | --- | --- |
| R1 | 架构解耦与核心性能基线 | v6.2.0 | HTTP 层按资源域拆分、切词 DRY、近重复去重 −99.3% allocs、14 条基准、bundle 基线与门禁、前端请求契约化 |
| R2 | Prompt 审计与 Guardrails | v6.3.0 | `internal/prompt` 注册表（17 条、版本/哈希/采样）、`GET /api/v1/prompts`、Prompt 审计页、采样参数真正透传 |
| R3 | 安全与异常处理加固 | v6.4.0 | 安全响应头 + CSP、双档令牌桶限流、上传内容嗅探/文件名净化、SSE 空闲心跳与写锁、前端请求超时 |
| R4 | UI/UX 与响应式 | v6.5.0 | 命令面板（⌘K）、阅读器窄屏抽屉、PDF 渲染竞态与 worker 生命周期修复、引用 bbox 随页失效、a11y |
| R5 | 自动化测试与压测 | v6.6.0 | Playwright E2E 接入 CI、benchmark 回归门禁、SSE 并发压测模拟与报告 |
| R6 | 部署文档与答辩材料 | v6.7.0 | 生产 runbook、架构文档、答辩 Q&A/Demo 脚本/证据矩阵、最终发布 |

---

## R1 · 架构解耦与核心性能基线（v6.2.0）

**审计发现**
- `apps/api/internal/api/server.go` 2049 行，混装 6 个资源域共 50 个 handler
  + 20 个 DTO 转换 + prompt 构造；改一个端点要在同一个文件里翻几百行。
- 切词规则在两处各写一遍（`tokenize` 与 `knowledge/fingerprint.go`），
  已经出现"检索去重 vs 指纹保留词频"的语义分叉风险。
- 近重复去重每次检索给每个 5-gram 新建字符串，一次 top-50 去重 3000+ allocs。
- 前端 15 处手写 `fetch` 绕开契约客户端，错误被包成普通 Error，
  RFC7807 的 `type` 丢失、界面显示原始 JSON。
- 仓库 0 条 benchmark、前端体积无基线；ADR-001 承诺的 `internal/arch_test.go` 不存在。

**讨论共识**
- 只做纯搬运拆分（同包不同文件），不拆子包；用 AST + `go doc` + 测试三重验证行为不变。
- 切词收敛到 `tokenize.Tokens`（保留词频），`Text/Query` 在其上去重。
- 去重改为"排序去重的 64 位 FNV 哈希 + 双指针 Jaccard"，旧实现留作测试对照。
- 前端请求统一走 `@congorag/api-client` + 一个新 `unwrap`，保留两条 SSE 的 fetch。

**落地与清理**
- 新增 `convert.go` + 6 个 `handler_*.go`；`server.go` 缩到外壳与公共工具（ADR-009）。
- 删除 `knowledge/fingerprint.go` 里重复的切词实现（DRY）。
- 新增 `internal/arch_test.go`：允许依赖边表、gin/状态码守卫、白名单过期即失败。
- 新增 12 条生产基准 + 2 条对照、`make bench`、`docs/performance.md`、
  `scripts/bundle-report.mjs` + 体积基线 + `make bundle-check`。
- 前端删除手写 `DocumentReference` / `WritingKind` 副本，新增 `lib/apiResult.ts`。

**验证**：Go 全绿（87 个函数体逐字节一致）、前端 28 文件/179 用例、bundle 预算内；
去重对照 212,455→161,184 ns、3,354→22 allocs/op。

**提交**：`75a0445` + `6c2881b`（bundle 基线），tag `v6.2.0`。

---

## R2 · Prompt 审计与 Guardrails（v6.3.0）

**审计发现**
- 提示词散落在 6 个文件里，无版本、无哈希、无变更记录；答辩要求的
  "Prompt 审计记录"只能靠人工抄写。
- 调用方传的 `WithTemperature(0.1)` 被 Eino 适配器 `_ = opts` 丢弃，
  文档记录的采样参数与运行时不一致——审计表格与实现对不上。

**讨论共识**
- 新建叶子包 `internal/prompt`：提示词是受版本管理的代码，不是运行时配置；
  只读 API + 只读界面，写操作走 code review。
- 哈希由服务端按内容现算（不落库），避免"改文本忘更新哈希"的静默不一致。
- 先修采样透传，再让注册表登记采样参数，否则审计口径仍然失真。

**落地与清理**
- 17 条提示词收敛进注册表（chat.system 保留真实 v1→v2 与 grounding 指标），
  5 处调用点改为 `Render/MustRender`；启动时 `Validate` 拒绝坏注册表。
- 新增 `GET /api/v1/prompts` + 六个 schema；Go/TS 生成物同步。
- Eino 适配器用 `WithExtraFields({temperature})` / `WithMaxCompletionTokens`
  真正透传；`resolveCallConfig` 可单测。
- 新增「Prompt 审计」页面（版本切换/哈希/采样/变量/代码位置，只读）。

**验证**：后端 777 用例全绿；前端 29 文件/185 用例；审计页截图来自真实注册表数据；
codegen 幂等、契约 lint 通过。

**提交**：`ea79901` + `94d8c6c`，tag `v6.3.0`。

---

## R3 · 安全与异常处理加固（v6.4.0）

**审计发现**
- 没有安全响应头、没有限流；公网演示形态完全依赖 nginx。
- 上传只查扩展名：改名成 `.pdf` 的 HTML 会一路存盘、解析失败、留一个 failed；
  文件名可含路径与控制字符。
- SSE 没有心跳，长时间等模型时反代/浏览器可能掐断连接；写帧路径没有并发保护。
- 前端非流式请求没有超时，连接假死会让界面永远转圈。

**讨论共识**
- 防御放在 Go 中间件而不是只写 nginx：本机 `make dev` 路径同样要生效。
- 限流两档（general / expensive），默认开启；配置非法必须启动失败，不能静默失效。
- 上传"在最早能说的一层说"：内容嗅探放在落盘之前。
- 心跳用 SSE 注释帧（不推进 lastEventId），写入口共用一把锁。

**落地与清理**
- `internal/platform/security.go`（CSP/nosniff/DENY/COOP 等，含主题引导脚本 sha256
  与防漂移测试）；`ratelimit.go`（令牌桶、懒清理、XFF 只在回环直连时采用）。
- `internal/knowledge/upload_validation.go`（扩展名 + 内容嗅探 + 文件名净化）。
- `sse.go` 心跳 + 写锁 + `defer sink.Close()`。
- `packages/api-client` 非流式请求 120s 超时。
- SECURITY.md 扩写；docs/sse-protocol.md 心跳章节；.env.example 与 README 配置表。

**验证**：Go 全绿（platform 新增 22 条测试）、前端 31 文件/205 用例、
契约版本 6.4.0 且生成物无 diff。

**提交**：`7da20e5`，tag `v6.4.0`。

---

## R4 · UI/UX 与响应式（v6.5.0）

**审计发现（R1 遗留风险成真）**
- 阅读器 <1024px 的目录/翻译按钮点了没反应（桌面栏 hidden 但按钮仍显示）。
- PDF 渲染不 cancel 旧任务，快速翻页会把取消异常显示成"加载失败"；
  PDF worker 模块级单例在 destroy 后被复用，离开再进入直接报错。
- 引用 bbox 高亮不随页失效；目录 active 计算 O(n²)；canvas 无文本替代。

**讨论共识**
- 先修缺陷再谈美化：阅读器是评分 e 的核心，假按钮比"不漂亮"更致命。
- 命令面板是低成本高感知的交互创新：⌘K + 子序列过滤 + 键盘导航，
  但必须懒加载，不能把 hooks 共享 chunk 吸进入口。
- 纯函数下沉 `lib/reader.ts` 以便单测，组件只留生命周期。

**落地与清理**
- 新增 `CommandPalette`（懒加载 + 空闲预取 + 快捷键注册在布局层）。
- 阅读器重写：抽屉化窄屏、渲染任务取消、worker 生命周期、bbox 随页、
  二分目录、a11y（role=img/status、h2、语义色 token）。
- 新增 `lib/reader.ts` + 20 个边界单测；命令面板 6 个交互测试。

**验证**：前端 32 文件/214 用例、lint 0、bundle ±0.69% 内；真实 PDF + Playwright
截图（命令面板/移动端目录/bbox）入库 `docs/assets/`。

**提交**：`faa0e2b`，tag `v6.5.0`。

---

## R5 · 自动化测试与高并发压测（v6.6.0）

**审计发现**
- 没有 E2E：页面级行为测试只覆盖 3/12 页，landing 静态页零测试。
- benchmark 只有文档基线，没有回归判据；首屏体积有基线但没有 CI 门禁执行。
- 没有压测：SSE 并发容量、TTFB 分位数、解析路径成本都是空白。

**落地**（详细数字见 `docs/loadtest-report.md` 与 `docs/quality-baseline.md`）
- Playwright E2E（全量 mock API）+ CI job；命令面板/移动端溢出/console error 全断言。
- `scripts/bench-gate.mjs` + `benchmarks/baseline.json`：allocs 严格、
  B/op +5%、ns/op +30% 且绝对值 >200ns 才失败（共享 runner 降噪）。
- `ops/loadtest/stream-load.mjs`：mock SSE 压测 + 真实目标模式，
  25/50/100 并发三档，输出 TTFB p50/p95/p99、事件吞吐、错误数、RSS。

**验证**：见交付文档中的实测表格；`make test-e2e` / `make bench-gate` /
`make loadtest` 三条命令可复跑。

**提交**：（见下方版本表）

---

## R6 · 部署文档与答辩材料（v6.7.0）

**落地**
- `docs/deployment.md`：生产 runbook（配置/反代/迁移/升级回滚/备份/故障处置/验收）。
- `docs/architecture.md`：分层与边界、包依赖图、五条关键请求流、数据模型、ADR 索引。
- `docs/defense/`：答辩 Q&A（14 问）、7 分钟 Demo 脚本、评分证据矩阵。
- README 最终对齐；CHANGELOG 与 tag 收口；bundle 基线刷新到最终提交。

**验证**：文档内命令逐条可执行；链接检查通过；最终 tag 与契约版本一致。

**提交**：（见下方版本表）

---

## 版本与提交索引

| 版本 | tag | 主题 | 关键提交 |
| --- | --- | --- | --- |
| 6.2.0 | `v6.2.0` | 架构解耦与性能基线 | `75a0445`、`6c2881b` |
| 6.3.0 | `v6.3.0` | Prompt 审计注册表 | `ea79901`、`94d8c6c` |
| 6.4.0 | `v6.4.0` | 安全与异常加固 | `7da20e5` |
| 6.5.0 | `v6.5.0` | UI/UX 与响应式 | `faa0e2b` |
| 6.6.0 | `v6.6.0` | 自动化测试与压测 | （R5 提交） |
| 6.7.0 | `v6.7.0` | 部署文档与答辩材料 | （R6 提交） |
