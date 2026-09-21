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
| **新增两条迁移** | `0006_idempotency_replay`、`0007_agent_tool_calling`。**升级前必须先跑 `make migrate-up`**——漏跑的直接后果是聊天整条路径 500 |
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

[Unreleased]: https://github.com/XiaoleC05/CongoRAG/compare/v3.0...HEAD
[3.0]: https://github.com/XiaoleC05/CongoRAG/compare/v2.0...v3.0
[2.0]: https://github.com/XiaoleC05/CongoRAG/compare/v1.0...v2.0
[1.0]: https://github.com/XiaoleC05/CongoRAG/releases/tag/v1.0
