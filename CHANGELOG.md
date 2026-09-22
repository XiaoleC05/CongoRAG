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

[5.0]: https://github.com/XiaoleC05/CongoRAG/compare/v3.0...v5.0
[3.0]: https://github.com/XiaoleC05/CongoRAG/compare/v2.0...v3.0
[2.0]: https://github.com/XiaoleC05/CongoRAG/compare/v1.0...v2.0
[1.0]: https://github.com/XiaoleC05/CongoRAG/releases/tag/v1.0
