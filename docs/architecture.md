# 系统架构

- 适用版本：v6.5.0 起（写作时契约版本 v6.5.0、39 个路径 / 51 个 operation；以 `contracts/openapi.yaml` 为准）
- 本文回答：系统由哪些进程与包组成、边界在哪、请求怎么流动、数据长什么样、设计取舍记录在哪
- 事实来源：代码与迁移文件（文中给路径）；设计依据在 `docs/adr/`；部署与排障在 `docs/deployment.md`
- 约定：本文不复述字段清单——字段以 `migrations/*.up.sql` 与契约为准

## 1. 系统全景

~~~mermaid
flowchart LR
  subgraph 进程外
    B[浏览器<br/>React 19 SPA]
    LLM[模型服务<br/>OpenAI 兼容 API（BYOK）]
  end
  subgraph api 进程
    MW[中间件链<br/>RequestID/SecurityHeaders/Recovery/OriginCheck/RateLimit]
    H[handler 层<br/>api 包]
    UC[业务用例<br/>conversation/agent/knowledge/llm/retrieval/ctxmgr]
    RSPA[内嵌 SPA<br/>go:embed 产物]
  end
  subgraph worker 进程
    RW[River 消费端<br/>文档处理 + 周期任务]
  end
  PDF[pdf-service<br/>FastAPI /parse]
  PG[(PostgreSQL 18 + pgvector<br/>业务表 + 向量 + river_job)]
  B -->|REST / SSE| MW --> H --> UC
  B --> RSPA
  UC --> PG
  UC -->|embedding / chat| LLM
  UC -->|解析 PDF| PDF
  RW --> PG
  RW --> PDF
  RW -->|embedding| LLM
~~~

运行时边界：

| 单元 | 端口 | 职责 | 装配根 |
| --- | --- | --- | --- |
| api | 3210（默认回环） | HTTP/SSE、SPA 静态资源、同步业务、Agent 执行（SSE 生命周期内） | `apps/api/internal/app/app.go` |
| worker | 无 | River 消费：文档解析/切分/embedding/索引 + 周期维护 | `apps/worker/internal/app/app.go` |
| pdf-service | 8000（compose 内网） | PDF 学术解析：标题/正文/表格/公式/参考文献，带页码/坐标/章节 | `sidecars/pdf-service/` |
| PostgreSQL | 5432 | 唯一 source of truth：业务数据 + 向量（halfvec）+ 队列（River 表） | `migrations/` + `deployments/` |

一个刻意的事实：**api 与 worker 共用同一份数据库连接串和同一个数据卷**。向量、分块、原始文件是配对的（库里有向量、卷里有原文），这决定了备份/升级/回滚必须两者一起做（见 `docs/deployment.md` §6）。

## 2. 分层与边界（ADR-001）

**模块化单体，不是微服务。** 一个 api 进程 + 一个 worker 进程，模块边界用 Go 包表达。取舍（ADR-001）是：

- 部署边界是**本地单机单用户**，微服务要解决的"多团队独立发布/按负载扩容"在这里不存在；
- 业务数据、向量、队列全在一个 PostgreSQL 里，"写业务记录 + 入队"能放在**同一个事务**（`internal/knowledge/river.go` 的 `EnqueueProcessing` 要求 `platform.Querier` 实际是 `pgx.Tx`，就是这条约束的代码化）；
- 代价：两个装配根有十几行重复接线（刻意接受，不为消重造共享装配包）；单机不横向扩展。

分层（箭头 = 允许的依赖方向）：

~~~text
apps/api、apps/worker（装配根：唯一 new 具体类型的地方）
        ↓
apps/api/internal/api（HTTP 边界：gin、路由、DTO、SSE 写出）
        ↓
internal/<业务包>（conversation/agent/knowledge/llm/retrieval/ctxmgr/prompt …）
        ↓
internal/platform（配置/DB/日志/中间件/加密/SSE 错误分类/调度）  internal/domain（中性类型，叶子）
~~~

规则（不是口头约定，大部分有编译期/测试期守卫）：

1. **业务包不知道 HTTP**：不 import gin、不知道 HTTP 状态码；错误用 `platform` 的 sentinel 往上抛，由 `platform.Classify` 和 HTTP 层映射。唯一的例外是 `platform` 自己的中间件/Problem/SSE 错误分类（它必须认识 gin 与状态码），以及 conversation 的 `eventErrorClass`（见 §3 例外表）。
2. **跨包"我需要什么"由消费方声明 port**，实现方零 import 边（例子：`llm.DocumentReindexer` 由 llm 声明、`knowledge.Usecase` 实现，连接点只在装配根）。
3. **两个进程各自的装配根**：不在 `internal/` 造共享装配包（会把 8 个包变成 9 个并让两个进程绑死）。
4. **契约是唯一真相**：路由、请求/响应形状、错误体都由 `contracts/openapi.yaml` 生成（`generated.go` 不手改）；版本规范见 ADR-002（契约版本 = 产品版本）。
5. **internal 不得 import apps**：库层不能依赖进程接线方式。

## 3. 包依赖图

下面这张图是 `internal/arch_test.go` 里 `allowedInternalEdges` 的**镜像**。**可执行的真值是那张表，不是这张图**——新增一条 import 边会先让 `go test ./internal/` 失败，要求同时改表与 ADR。

~~~mermaid
graph TD
  domain([domain 叶子：中性类型])
  platform([platform 叶子：基础设施])
  tokenize([tokenize 叶子：切词])
  grounding([grounding 叶子：引用落地校验])
  prompt([prompt 叶子：提示词注册表])
  testdb([testdb：只给 *_test.go])

  pdfparse --> domain
  visualize --> platform
  arxiv --> platform
  llm --> domain
  llm --> platform
  llm --> prompt
  recommend --> arxiv
  recommend --> platform
  knowledge --> domain
  knowledge --> platform
  knowledge --> tokenize
  retrieval --> domain
  retrieval --> llm
  retrieval --> platform
  retrieval --> tokenize
  ctxmgr --> domain
  ctxmgr --> llm
  ctxmgr --> prompt
  conversation --> ctxmgr
  conversation --> domain
  conversation --> grounding
  conversation --> llm
  conversation --> platform
  conversation --> prompt
  agent --> arxiv
  agent --> domain
  agent --> llm
  agent --> platform
  agent -. 白名单例外：仅 Event/EventSink .-> conversation
~~~

包的职责（一级目录 ⇒ 一句话）：

| 包 | 职责 | 关键文件 |
| --- | --- | --- |
| `domain` | 跨包中性类型：Chunk/Citation/SearchRequest/Role/来源枚举 | `domain.go` |
| `platform` | 配置、pgx 接口（Querier/Pinger/TxManager）、日志、中间件（RequestID/OriginCheck/Recovery/SecurityHeaders/RateLimit）、RFC7807 Problem、SSE 错误分类、AES-GCM SecretBox、River 周期调度桥、游标分页 | `config.go`、`middleware.go`、`ratelimit.go`、`security.go`、`secretbox.go`、`problem.go` |
| `tokenize` | 中文 bigram + 英文词切分，生成 FTS 查询（`Text`/`Tokens`/`Query`） | `tokenize.go` |
| `grounding` | 回答按句校验 [n] 引用是否覆盖 citation（纯规则，零模型调用） | `grounding.go` |
| `pdfparse` | PDF sidecar 的 HTTP 客户端（`POST /parse`，超时注入） | `pdfparse.go` |
| `arxiv` | arXiv API 客户端（Atom 解析） | `arxiv.go` |
| `recommend` | 推荐排序（相关性/新近/GitHub/会议线索）+ 赞踩存储 | `recommend.go` |
| `visualize` | CSV→图表结构 + Mermaid/TikZ/Matplotlib 代码 + 确定性图注 | `visualize.go` |
| `prompt` | 提示词注册表：版本、变量、内容 hash、采样参数、效果记录；生产代码只从这里取文本 | `prompt.go` |
| `llm` | provider/model 配置（BYOK，Key 加密落库）、探测 embedding 维度、Eino 适配器、tokenizer/tiktoken 词表、用量记账 | `usecase.go`、`eino.go`、`tokenizer.go`、`vocab.go` |
| `retrieval` | 混合检索：query embedding + 向量 + 词法 FTS + RRF 融合 + 近重复抑制；分块 embedding/落库 | `usecase.go`、`postgres.go`、`dedupe.go` |
| `ctxmgr` | 上下文预算阶梯（固定区/可压缩区/删除区）与摘要压缩 | `usecase.go`、`compressor.go` |
| `knowledge` | 知识库/文档 CRUD、上传落盘、River 处理流水线、SimHash 指纹去重、引用图与参考文献解析、孤儿文件对账 | `usecase.go`、`pipeline.go`、`river.go`、`graph.go`、`fingerprint.go` |
| `conversation` | 会话/消息/事件、SSE 事件的生产侧、幂等重放、长期记忆/摘要/偏好、事件回收 | `usecase.go`、`postgres.go`、`memory.go`、`retention.go` |
| `agent` | Agent 配置、run 生命周期、Eino ADK ReAct 执行、工具、checkpoint、恢复/取消、保留策略 | `usecase.go`、`eino_adk.go`、`port.go`、`retention.go` |
| `testdb` | 集成测试用一次性 PostgreSQL 容器（只允许 *_test.go import） | `testdb.go` |

**守卫与例外**（全部在 `internal/arch_test.go`；`go test ./internal/ -run TestArch -v` 可看）：

| 规则 | 当前例外 | 为什么允许 / 怎么消掉 |
| --- | --- | --- |
| internal 包之间的 import 必须命中方向表 | **agent → conversation**（只允许 `Event`/`EventSink` 两个符号） | 两条 SSE 流共享的边界类型；正解是把 Event/EventSink 迁到 `domain`（或新增 `internal/stream` 叶包）后删除白名单 |
| internal 里只有 `platform` 可以 import gin | platform（中间件/Problem/SSE 错误分类） | 它是 internal 与 HTTP 框架的唯一边界 |
| 业务包不得使用 `net/http` 状态码常量 | platform；出网客户端 arxiv/llm/pdfparse；conversation 的 `eventErrorClass`（函数级例外） | 出网客户端要判断上游 `resp.StatusCode`；conversation 那处应在下一次异常加固时收敛到 `platform.Classify` |
| `internal` 不得 import `apps` | 无 | 库层不依赖进程接线 |
| `internal/testdb` 只能被测试文件 import | 无 | 防止 testcontainers 进生产二进制 |

## 4. 关键请求流

### 4.1 流式问答（SSE）

~~~mermaid
sequenceDiagram
  participant B as 浏览器
  participant H as api/handler_conversation.go
  participant C as conversation.Usecase
  participant R as retrieval.Usecase
  participant X as ctxmgr.Usecase
  participant L as llm.Usecase
  participant G as grounding
  participant P as PostgreSQL
  B->>H: POST /api/v1/conversations/{id}/messages（可选 Idempotency-Key）
  H->>H: bindJSON（1 MiB 上限）、幂等键长度校验（在切 SSE 之前）
  H->>C: Send(ctx, convID, text, key, sseSink)
  C->>P: 事务：会话锁 + conversation_counters 发号 + 用户消息 + assistant 占位(streaming)
  C->>R: Search(kb, text, topK=5)
  R->>R: embedding 查询 → 向量召回 + 词法召回(tokenize.Query) → RRF(k=60) 融合 → 近重复抑制
  C->>X: Build(预算阶梯：固定区/压缩/删除 memory/chunk)
  C-->>B: 先发 citation 事件（chunkId/page/bbox/sectionPath）
  C->>L: Stream(system prompt + 证据 + 历史)
  L-->>C: 增量 token
  C-->>B: token 事件（每 500ms checkpoint 落库一次内容）
  C->>G: Verify(answer, citations)
  C-->>B: grounding 事件 → done 事件
  C->>P: 落 assistant 终态 + citations
~~~

要点（文件）：幂等命中时**不重新生成**，按真实 `event_id` 补发同一轮事件（ADR-008）；事件先落库再下发，发号与写入同事务（`internal/conversation/postgres.go` 的 `NextEventID`）；帧格式与事件类型见 `docs/sse-protocol.md`；断线后客户端用 `GET /conversations/{id}/events?after_event_id=N` 补发，补完即结束响应，不保持连接等新事件。没有证据时**不调用模型**，走固定拒答文案并照常发 token/grounding(valid=false)/done。

### 4.2 上传 → 解析 → 切分 → embedding → 索引

~~~mermaid
flowchart TD
  U[POST /knowledge-bases/:id/documents<br/>multipart, ≤32 MiB] --> V[校验扩展名 .md/.markdown/.txt/.pdf、文件名 ≤255]
  V --> F[内容 SimHash 指纹 + 与库内距离比较<br/>distance ≤14 → 标重复告警]
  F --> W[写临时文件 → fsync → rename 到数据卷]
  W --> T[事务：插入 documents(status=queued) + River 入队]
  T --> A[202 Accepted, 返回 Document]
  T --> Q[(river_job)]
  W2[worker: DocumentProcessingWorker<br/>单任务上限 30 分钟] --> P1[ProcessDocument: queued→processing CAS]
  Q --> W2
  P1 --> P2{扩展名}
  P2 -->|pdf| PDF[pdfparse → sidecar /parse<br/>超时 CONGORAG_PDF_TIMEOUT]
  P2 -->|md/txt| TXT[parseAndChunk]
  PDF --> CH[切分：目标 ≤1200 字符/块<br/>+ 首页元信息块]
  TXT --> CH
  CH --> EM[EmbedChunks：事务外，每批 256 条]
  EM --> SV[事务：ReplaceChunks（先删后插）+ status=ready]
  SV --> DONE[检索可见]
  EM -.失败.-> RETRY[交回 River 重试]
  RETRY --> P1
  RETRY -.最后一次 attempt.-> FAILED[status=failed]
~~~

事务边界是这里最重要的一条：**embedding 网络调用在事务外**，写库（删旧块/插新块）与置 `ready` 在同一事务里（`internal/knowledge/usecase.go` 的 ProcessDocument 注释，issue #97）。否则事务会长时间占着连接池，10 个 worker 就能把池攥干。另：孤儿文件对账周期任务（每 10 分钟、宽限 15 分钟）清理"写了文件但没落库"的残留。

### 4.3 Agent run（工具循环 / checkpoint / 取消与恢复）

~~~mermaid
sequenceDiagram
  participant B as 浏览器
  participant H as api/handler_agent.go
  participant A as agent.Usecase
  participant E as Eino ADK ReAct 图
  participant T as 工具（知识库/arXiv/计算器/会话搜索）
  participant P as PostgreSQL
  B->>H: POST /api/v1/agents/{id}/runs（可选 Idempotency-Key）
  H->>A: Start(input ≤8000, key, sseSink)
  A->>P: prepareRun：Agent 配置 + chat 模型 + tool_calling 能力门控
  A->>P: claimRun：幂等键预留（输入指纹）+ 插入 run(pending)
  A-->>B: run_started（新建与重放都发，data={runId}）
  loop 最多 20 轮
    E-->>A: token / tool_call 事件
    A->>P: 一轮 llm step（本轮首个助手事件时开行，结束补终态）
    A->>P: tool step 在工具执行**之前**插入(running)
    E->>T: 执行工具（只读/低风险）
    T-->>E: 结果
    A->>P: ① 独立事务写 tool_effect_log（先账本）② 再补 tool step 终态（后结果）
    A->>P: checkpoint（Step 边界：state_snapshot + current_step）
    A-->>B: tool_call / tool_result / token 事件
  end
  A->>P: run 终态 + 释放幂等键
  A-->>B: done（或 error 帧）
~~~

取消：`POST /api/v1/runs/{runId}/cancel` → 取消 run 的 ctx 并等待收尾写入完成（返回**取消生效后**的记录）。恢复：`POST /runs/{runId}/resume` → `PrepareResume` 先校验（run 状态、`state_schema_version`、工具可重放性），再开 SSE；恢复的判定依据是 ADR-007 的三行崩溃表 + `tool_effect_log`（顺序：先写账本、后写结果，这个顺序不能反）。升级时的在途 run 在 api 启动扫描里被标 `interrupted`（ADR-007 决策二），之后可由用户手动恢复。

### 4.4 Prompt 注册表读取

~~~mermaid
flowchart LR
  UI[前端 Prompt 审计页] -->|GET /api/v1/prompts| H[api/handler_prompt.go]
  H --> C[prompt.Catalog<br/>排序 + 深拷贝]
  C --> REG[internal/prompt 注册表<br/>Entry: key/title/description/kind/variables/codeRefs/versions/metrics]
  start[[api 启动]] --> V[prompt.Validate]
  V --> REG
  CONV[conversation] --> RENDER[prompt.Render / MustRender]
  CTX[ctxmgr] --> RENDER
  LLM[llm] --> RENDER
  RENDER --> REG
~~~

注册表是**叶子包**（不 import 任何 internal 包），生产代码只通过它取文本；版本包含内容、变更原因、生效日期、采样参数与效果记录，内容用 SHA-256 全量哈希标识（`internal/prompt/prompt.go`），启动时 `apps/api/internal/app/app.go` 调 `prompt.Validate()` 校验注册表自身。审计口径与历史版本另见 `docs/prompt-audit.md` 与 ADR-010。

### 4.5 中间件链与路由

~~~mermaid
flowchart LR
  REQ[HTTP 请求] --> RID[RequestID<br/>X-Request-ID + ctx]
  RID --> SEC[SecurityHeaders<br/>nosniff/DENY/no-referrer/COOP/CSP]
  SEC --> REC[Recovery<br/>panic→Problem；SSE 已开始则补 error 帧]
  REC --> ORIGIN[OriginCheck<br/>Host/Origin 必须回环]
  ORIGIN -->|403 forbidden_host/origin| END1[Problem 响应]
  ORIGIN --> RL[RateLimit<br/>仅 /api/；general/expensive 两档]
  RL -->|429 rate_limited + Retry-After| END2[Problem 响应]
  RL --> ROUTER[generated.go 按契约挂载]
  ROUTER --> HANDLER[handler_*.go]
  ROUTER -->|未匹配| SPA[MountSPA：静态文件/index.html；/api/ 前缀→404 Problem]
~~~

顺序的理由（`apps/api/internal/app/app.go` 中有逐条注释）：SecurityHeaders 必须在 Recovery 之前，panic 的 500 Problem 才会带安全头；RateLimit 必须在 OriginCheck 之后，跨站请求先被 403、不消耗本机用户的额度；Recovery 放在业务中间件之前，兜住它们的 panic。限流细节（只对 /api/、两档独立、expensive 路由表）见 `internal/platform/ratelimit.go` 与 `docs/deployment.md` §2。

## 5. 数据模型概要

两套迁移系统并存（ADR-003）：业务表由 golang-migrate 管（`migrations/`，两套迁移的顺序在 `deployments/docker/migrate-entrypoint.sh`），River 自己的队列表（`river_job` 等）由 river CLI 管、文件嵌在它的二进制里。业务表清单（表名可在迁移文件里核对）：

| 表 | 用途 | 迁移 |
| --- | --- | --- |
| `knowledge_bases` / `documents` / `document_chunks` / `memories` | 知识库、文档（状态 queued/processing/ready/failed、page_count、parser、指纹与去重字段）、分块（正文、embedding、页码/bbox/章节/块类型、search_text）、长期记忆 | 0001，后续 0015/0016/0017/0018 扩展 |
| `llm_providers` / `llm_models` | BYOK provider（base URL、加密后的 API Key）与模型（kind、能力位、上下文窗口、维度、tokenizer） | 0002（0007 扩展 tool_calling） |
| `conversations` / `messages` / `conversation_events` / `conversation_counters` / `conversation_summaries` / `idempotency_keys` / `token_usage` | 会话、消息（role/内容/状态/引用）、SSE 事件与按会话发号计数器、摘要、幂等键、模型用量 | 0003/0004/0006/0011/0012/0014/0017 |
| `tools` / `agents` / `agent_runs` / `agent_run_steps` / `run_events` / `run_counters` / `tool_effect_log` | 工具元数据、Agent 配置、run（状态/checkpoint/schema 版本/当前步）、步骤轨迹、run 事件与按 run 发号计数器、副作用账本 | 0005/0009/0010（0007 扩展） |
| `arxiv_feedback` | 推荐赞/踩持久化（down 过滤） | 0019 |

存储要点：

- **向量列一律 `halfvec` + `halfvec_cosine_ops` HNSW 索引**（ADR-004）；embedding 维度由 BYOK 探测决定（上限见 `internal/llm/usecase.go` 的 `maxEmbeddingDim`），换 embedding 模型需要用户确认后重建（409 `embedding_change_requires_reindex`）。
- **词法检索**用 `search_text`（写入时按 `tokenize` 规则算一次）+ FTS，向量/词法两路各自召回后用 RRF 融合（k=60）。
- **事件发号维度跟流的所有者走**：聊天按会话（`conversation_events` + `conversation_counters`），Agent 按 run（`run_events` + `run_counters`）；两张表不合并（`docs/sse-protocol.md`「两张事件表」）。
- **保留窗口**：幂等键与两类事件 24 小时（`platform.IdempotencyKeyTTL`），tool_effect_log 在 run 终态后再留 24 小时，checkpoint 终态后 24 小时；回收由 worker 的周期任务执行。

## 6. ADR 索引

| 编号 | 标题 | 一句话结论 |
| --- | --- | --- |
| [ADR-001](adr/001-modular-monolith.md) | 模块化单体 + 单 Worker | 一个 api 进程 + 一个 worker 进程，模块边界用 Go 包；不拆微服务 |
| [ADR-002](adr/002-contract-versioning.md) | 契约版本 | 契约版本 = 产品版本（五处同一个数）；兼容性边界是路径里的 `/api/v1`，不是版本号 |
| [ADR-003](adr/003-two-migration-systems.md) | 两套迁移系统 | 业务表 golang-migrate 管、River 队列表 river CLI 管，不强行统一 |
| [ADR-004](adr/004-halfvec-embeddings.md) | halfvec | 向量列一律 halfvec，HNSW 索引用 halfvec_cosine_ops |
| [ADR-005](adr/005-sse-over-fetch.md) | SSE over fetch | 服务端按标准 SSE 写帧；客户端用 fetch + ReadableStream 手工解析（不用 EventSource，才能带 Idempotency-Key） |
| [ADR-006](adr/006-checkpoint-layering.md) | checkpoint 分层 | 不接 Eino 那层的 checkpoint，业务层 CheckpointStore 是恢复的唯一权威（Step 边界） |
| [ADR-007](adr/007-recovery-semantics.md) | 恢复语义 | 三行崩溃表：step 仍 running 且无账本→按 ToolMetadata 决定重放；step 仍 running 但账本已有记录→一律拒绝；结果已落库→继续；升级时在途 run 标 interrupted |
| [ADR-008](adr/008-idempotency-replay-window.md) | 幂等重放窗口 | run 端点最小快照 = run_id（首帧 run_started），其余走事件补发；键保留 24h、作用域 endpoint+key |
| [ADR-009](adr/009-api-handler-layout.md) | handler 布局 | api 包内按资源域分文件（server.go/convert.go/handler_*.go），不拆子包 |
| [ADR-010](adr/010-prompt-registry.md) | Prompt 注册表 | 新建叶子包 `internal/prompt` 作为提示词唯一真相，生产代码只通过它取文本 |

## 7. 关键不变式

1. **契约是唯一真相**：路由/形状/错误体来自 `contracts/openapi.yaml`；生成物必须提交，CI 用 `make generate + git diff --exit-code` 守；契约版本 = 产品版本（ADR-002）。
2. **提示词唯一真相**：所有 prompt 文本与采样参数在 `internal/prompt` 注册表里（ADR-010）；启动校验、内容哈希可审计。
3. **依赖方向**：`internal/arch_test.go` 的白名单是唯一可执行真值；业务包不 import gin、不知道 HTTP 状态码（例外见 §3）；internal 不 import apps；testdb 只给测试。
4. **两进程、两个装配根**：具体类型只在 `apps/*/internal/app/app.go` 里 new；不为消重造共享装配包。
5. **数据库是 source of truth**：业务数据、向量、队列同库；"写业务记录 + 入队"同事务；**事务里不做模型/embedding 网络调用**（embedding 在事务外，写块与置 ready 同事务）。
6. **SSE 语义**：事件先落库再下发（发号与写入同事务）；`event_id` 单调递增、允许跳号；无 `id` 的 error 帧不推进客户端游标；注释帧（心跳）不带 id；断线补发"发完即结束"。
7. **幂等语义**：24h、作用域 = 端点（含资源 id）+ 键；命中时补发同一轮/同一 run 的已记录事件，不重新执行；run 首帧 `run_started` 带 run_id。
8. **恢复语义**：checkpoint 在业务层（Step 边界）；先写 `tool_effect_log` 再写步骤结果（顺序不能反）；拒绝自动重放的两个错误类型 `tool_effect_already_applied` / `replay_unsafe` 是产品语义的一部分。
9. **安全边界**：默认只绑回环 + OriginCheck；安全响应头与 CSP（内联主题脚本用 sha256 放行）；两档限流；上传类型白名单 + 32 MiB 上限；BYOK Key 用主密钥 AES-GCM 加密落库、任何响应不回显明文。
10. **数据配对**：原始文件（数据卷）与数据库记录/向量必须一起备份、一起回滚；主密钥与数据库一起迁移（否则 provider key 全废）。

## 8. 代码地图

| 路径 | 内容 |
| --- | --- |
| `apps/api/` | api 进程：`main.go`（go:embed 前端，只调用 app.Run）、`internal/app/`（装配根 + 优雅退出）、`internal/api/`（HTTP 边界：server.go 外壳、convert.go DTO 转换、handler_*.go 按资源域、generated.go 契约产物、sse.go、spa.go、problem.go） |
| `apps/worker/` | worker 进程：River 消费端（文档处理、文件清理、周期任务），无 HTTP |
| `internal/` | 业务与支撑包（见 §3 表） |
| `contracts/openapi.yaml` | 契约唯一真相（39 路径 / 51 operation） |
| `migrations/` | 业务表迁移（golang-migrate），River 表迁移在 river CLI 里 |
| `web/` | React 19 + Vite + Tailwind 前端源码；构建产物 go:embed 进 api |
| `landing/` | 公开首页（静态 HTML） |
| `sidecars/pdf-service/` | PDF 解析 sidecar（FastAPI + PyMuPDF + RapidOCR） |
| `deployments/` | 三份 compose（开发依赖/启动包/演示）与迁移入口 |
| `benchmarks/`、`docs/perf/` | 基准回归基线（bench-gate）与前端体积基线（bundle-check） |
| `docs/` | 架构、部署、升级、SSE 协议、测试、Prompt 审计、ADR、性能基线 |
| `scripts/` | 发布（release）、升级（upgrade）、回滚（rollback）、打包（startup-package）、基准门禁（bench-gate）、体积报告（bundle-report）、崩溃探针（crash-probe） |

## 9. 扩展指引

- **加端点**：改 `contracts/openapi.yaml` → `make generate`（Go + TS 两个产物，必须提交）→ 在对应 `handler_*.go` 实现、转换函数放 `convert.go` → 跑 `go test ./...` 与 `make test-contract`。
- **加提示词**：在 `internal/prompt/prompt.go` 的 `registry` 加 `Entry`（版本、变量、`CodeRefs`、`Sampling`、效果记录）→ 启动的 `prompt.Validate` 与 `prompt_test.go` 会校验 → 同步 `docs/prompt-audit.md`。
- **加 internal 包**：先想清楚方向（消费方声明 port 还是直接依赖）→ 在 `internal/arch_test.go` 的 `allowedInternalEdges` 登记边 → 若属于新架构决策，同步写 ADR。
- **加迁移**：`migrations/NNNN_xxx.{up,down}.sql` 成对写，两套迁移系统互不干扰；集成测试跑 `make test-integration`。
- **前端/体积**：改前端后 `pnpm --filter web build` → `make bundle-check`；纯函数性能用 `make bench-gate`。
- 发布/升级/回滚：见 `docs/releasing.md`、`docs/upgrading.md`、`docs/deployment.md`。

## 10. 已知架构债（写给自己/下一轮）

1. **HTTP 层仍有科研助手的编排**：writing/compare/survey/version-diff 等端点仍在 `apps/api/internal/api/handler_research.go` 里"检索 → 组装 → 调用模型"，R1 提出的 `internal/research` 用例层尚未落地（提示词文本已收敛到注册表）。
2. **agent → conversation 的类型例外**：只为 `Event/EventSink`；正解是迁到 `domain` 或 `internal/stream`，然后删白名单（守卫会强制清理过期条目）。
3. **conversation 的 HTTP 状态码例外**：`eventErrorClass` 直接返回 400/409，尚未收敛到 `platform.Classify`。
4. **platform 是唯一 gin 边界**：这让"internal 不 import gin"成立，但也意味着中间件与 Problem 的改动只能落在 platform；新增中间件时先看 `internal/arch_test.go` 的例外表。
5. **无 metrics 端点**：可观测性靠日志（request_id/agent_run_id）、/readyz 与业务接口；若要做长期监控需要新端点 + 契约 + 安全边界。
