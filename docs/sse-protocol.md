# SSE Wire Protocol

这份文档是流式协议的唯一规范来源——服务端的 SSE 写出（`apps/api/internal/api/sse.go`）
和前端的帧解析器（`web/src/lib/streamChat.ts`）都必须照这份文档实现，不是照对方的代码猜。

技术方案 §九 / 代码架构设计 §5.8 已经定了大方向，这里写清楚线路层的字节格式。

## 为什么不用浏览器原生 `EventSource`

`EventSource` 只支持 `GET` 请求、不能带自定义请求头（要发请求头的场景——
认证、以及幂等重放用的 `Idempotency-Key`——都用不了）、断线重连的策略
也不可控。改用 `fetch` +
`ReadableStream` 自己解析——代价是断线重连、`Last-Event-ID` 这些
`EventSource` 免费给的东西，全部要自己写（见"断线续传"一节）。

## 帧格式

标准 SSE 文本帧，每帧以**一个空行**结束：

```text
id: 42
event: token
data: {"text":"你好"}

id: 43
event: done
data: {}

```

- `id`：这条事件的 `event_id`（见下面的"事件与续传"），单调递增
- `event`：事件类型，见下一节
- `data`：一行 JSON，`event` 决定它的形状
- 心跳：**当前没有心跳。** 本文档早期版本写的是"服务端每 15 秒发一条注释行
  `: heartbeat\n\n`"，但那个定时器从来不存在——`apps/api/internal/api/sse.go`
  里没有任何 `time.Ticker`（`apps/api/internal/app/shutdown.go` 那条排空预算的
  注释也记着这件事）。本地直连、没有反向代理，所以保活本来也不需要。
  **客户端仍然必须忽略以 `:` 开头的行**：那是 SSE 规范里的注释，忽略它是解析器
  本来就该做的事（`web/src/lib/streamChat.ts` 照做了），将来真加上心跳时
  客户端不用跟着改。

**唯一的例外：`id` 可以缺席。** 有两条路径会发出没有 `id` 的 error 帧，
共同点是**这一帧没有对应的持久化事件**：

1. **事件没能落库**——分配 `event_id` 的那一步失败了（见
   `apps/api/internal/api/sse.go` 的写失败兜底分支）。
2. **运行根本没开始**——空输入、Agent 不存在、当前没有可用的聊天模型、
   模型不支持工具调用。这些失败发生在分配 `run_id` 之前，`run_events`
   里没有可挂的行（见 `internal/agent/usecase.go` 的 `emitUnpersisted`）。

它照 `event:` / `data:` 两行发出去，没有 `id`。**客户端必须容忍这一点，
而且不能因为收到它就推进续传游标**：这一帧不代表任何一条已持久化的事件，
拿它当游标会让断线续传从头跳过一批还没收到的事件。判据很简单——`id`
缺席的帧不参与发号，只用于把失败告诉用户。

**线路层怎么区分这两种帧**：`internal/agent` 用 `event_id = 0` 表示
"未持久化"，`sseSink.Emit` 见到 0 就整行省掉 `id:`。0 不是任何一条真实
事件的号——两张计数器表（`conversation_counters` / `run_counters`）的
`next_event_id` 都从 1 开始。

**一帧可能跨两次 `read()`**——TCP 不保证一次 `read` 刚好读到一个完整帧的边界。
解析器必须维护一个缓冲区，按 `\n\n` 切出完整帧，切不出来的部分留到下一次
`read()` 再拼。开发文档已经点过这个坑：先 `curl -N` 看裸流，再写解析代码。

## 事件类型

```text
run_started  Agent 运行的身份。data: {"runId": string}
             **只出现在 Agent 的运行流上（POST /api/v1/agents/{id}/runs 与
             POST /api/v1/runs/{runId}/resume），而且永远是首帧。**
             新建与幂等重放两种情况都会发，形状完全相同——客户端凭它拿到
             这次运行的 id，取消、run 级重订阅、跳转到运行详情都要用它。
             普通聊天没有 Run，不会出现这一帧。
token        文本增量。data: {"text": string}
citation     一次引用命中。data: {"chunkId": string, "documentId": string,
                              "filename": string, "snippet": string, "score": number}
tool_call    工具调用请求（M4-A 才会出现）。data: {"id": string, "name": string, "args": object}
tool_result  工具调用结果（M4-A 才会出现）。data: {"id": string, "result": unknown}
error        出错。data: {"type": string, "detail": string}——type 和 REST 的
             Problem.type 是同一套枚举（web/README.md 那张表），前端可以复用同一个
             错误文案映射，不用为流式错误另外维护一套文案
done         流正常结束。data: {}
```

`citation` 随流下发，不是等答案生成完才一次性给——这是产品能力表里
"citation 随流推送"那一条的字面实现。

## 事件与续传

**发号维度跟着流的所有者走**：普通聊天没有 Run（M4-A 才有），所以
`event_id` 按**会话**发号，不用全局自增（全局自增会导致跨会话的事件号
交错，续传时无法只针对一个会话过滤）。

发号用 `conversation_counters` 一行"下一个要发的号"，和写入
`conversation_events`／更新 assistant 消息**在同一个数据库事务**里递增
（`internal/conversation/postgres.go` 的 `NextEventID`）。

```sql
INSERT INTO conversation_counters (conversation_id, next_event_id)
VALUES ($1, 1)
ON CONFLICT (conversation_id) DO UPDATE
  SET next_event_id = conversation_counters.next_event_id + 1
RETURNING next_event_id;
```

### 事务回滚会在 event_id 序列里留下空洞——这是接受的取舍

计数器递增和它所属的那次写库操作在同一个事务里。如果事务回滚，
已经分配出去的号不会被下一次操作复用（不会有另一个事件顶着相同的
`event_id` 出现）。**后果是序列里可能有跳号**（比如 5 之后直接是 7），
但**不会有重复号或错位**。续传逻辑只需要保证"我请求 `after_event_id=N`
时，服务端把所有 `event_id > N` 的事件按顺序发给我"，空洞不影响这个语义——
客户端不应该假设 `event_id` 连续，只应该假设它单调递增。

（技术方案 §5.3 把这个取舍列成"二选一"：另一个选项是让计数器的递增独立于
业务事务提交，代价是要单独处理"计数成功但业务写入失败"的不一致，
复杂度换来的只是让空洞消失——权衡下来接受空洞更简单，所以选了这个。）

## 断线续传

```text
① 客户端记住收到的最后一个 event_id（lastEventId，内存变量，
   不是 EventSource 的 Last-Event-ID 请求头——我们没用 EventSource）
② 连接断开（网络抖动、页面刷新前的重连尝试）
③ 客户端重新发起请求：
     GET /api/v1/conversations/{id}/events?after_event_id={lastEventId}
     GET /api/v1/runs/{runId}/events?after_event_id={lastEventId}   ← Agent 运行
④ 服务端从对应的事件表按 event_id > after_event_id 查出补发的部分，
   发完就结束响应
```

### 两张事件表、两套编号（run 维度是 v4.0 加的）

**发号维度跟着流的所有者走**，所以两条路径各有自己的表与计数器：

| | 普通聊天 | Agent 运行 |
| --- | --- | --- |
| 事件表 | `conversation_events` | `run_events` |
| 计数器 | `conversation_counters` | `run_counters` |
| 编号空间 | 按**会话**发号 | 按 **run** 发号 |
| 重订阅 | `GET /conversations/{id}/events` | `GET /runs/{runId}/events` |

两张表并存、各自独立编号，**不搬迁、不合并**（项目文档 §8.6 定的方案）。
`run_events` 的 `event_id` 从 1 开始，所以"从头补发整个 run"就是
`after_event_id=0`。

**第 ④ 步发完历史就结束，不会继续持有连接等新事件。** 这一点与本文档早期
版本写的「发完历史再继续实时推送」不同——那条承诺从来没有被实现，而且
它对本地单机场景没有意义：持有连接等新事件，和客户端直接再发一次请求，
得到的结果是一样的。要看某一轮后续的内容，走「幂等与断线的闭环」那一节
的补发路径。

**`Last-Event-ID` 请求头不会被浏览器自动带上**——那是 `EventSource` 的行为，
`fetch` 什么都不会替你做。重连循环、记录 `lastEventId`、拼进请求 URL，
三件事都是前端自己的代码要做的（见 `web/src/lib/streamChat.ts`）。

## 幂等与断线的闭环

`POST /conversations/{id}/messages` 接受可选的 `Idempotency-Key` 请求头
（契约里已声明）。同一个会话带同一个键重复提交时，服务端**不重新执行**
生成，而是把那一轮已经记录的事件补发出来。

**这就是 `fetch` 而非 `EventSource` 的主要理由**——`EventSource` 发不了
自定义请求头（见本文开头）。

**Agent 运行那一侧是同一个机制**：`POST /api/v1/agents/{id}/runs` 也接受
`Idempotency-Key`，命中时补发那条 run 已经记录的事件，首帧是
`run_started`（带的是**原来那条 run 的 id**）。详见
`docs/adr/008-idempotency-replay-window.md`。

### 作用域与保留窗口

- **作用域 = 同一个端点（含资源 id）+ 同一个键。** 键落在
  `idempotency_keys` 表，主键是 `(endpoint, idempotency_key)`，而
  `endpoint` 里编了会话/Agent 的 id（`POST /api/v1/conversations/<uuid>/messages`、
  `POST /api/v1/agents/<uuid>/runs`）。同一个键在另一个会话、另一个 Agent
  上都不会命中。
- **保留 24 小时，两条路径共用同一个值**
  （`platform.IdempotencyKeyTTL`）。超过之后同一个键可以重新执行——这是
  有意的产品语义，因为隔了一天之后的重试本来就没有意义了。过期行在每次
  预留时顺手清掉。

### 命中时客户端收到什么

**同一轮事件的补发**，不是一条错误、也不是一个资源 id：

- 帧的类型、**真实的 `event_id`**、顺序都和原请求一致，所以客户端的
  续传游标仍然正确，`lastEventId` 的语义不变。
- 如果命中时这一轮**还没生成完**，服务端会边等边发，直到该轮出现终态
  事件（`done` 或 `error`）为止。等待有绝对上限（10 分钟），超时会推一条
  带真实 `event_id` 的 `error` 帧收场——静默结束响应会让客户端停在
  一个空流上。
- 同一个键配**不同的正文**会收到 `invalid_argument` 的 error 帧，而不是
  把上一轮的答案重放一遍。

### 不要再按旧写法用 `after_event_id=0` 重新订阅

本文档早期版本让客户端在命中幂等键后调
`GET .../events?after_event_id=0` 重新订阅。**那是错的**：`event_id` 是按
会话发号的（见上面「事件与续传」），`0` 会把此前每一轮的 token 全部重放
一遍，而客户端会把它们拼进同一个正在生成的气泡里。

补发所需的「本轮从哪条事件开始」由服务端在预留键时记下
（`idempotency_keys.first_event_id`），客户端不需要、也不应该自己算。

### 两条已知局限

- **两条路径的边界不同，别混。** `POST` 命中幂等键时这条补发**会边等边发**：
  它一直读到该轮出现终态事件（`done` / `error`）为止，所以「这一轮还没生成完」
  也能接着看。而 `GET /events` 不会——它补发完已有的历史就结束响应（见
  「断线续传」一节）。客户端不要指望 `/events` 帮它等后续内容。
- 同一个会话有两轮在并发（两个标签页、不同的键）时，另一轮的事件号会
  高于本轮的游标，补发可能混入它、并可能提前停在它的 `done` 上。这是
  「事件号按会话发号」这个既有设计的性质，不是幂等引入的。

## 服务端实现要点

- 每条事件写出后立刻 `Flush()`——不 flush 的话事件会攒在 Go 的
  buffered writer 里，客户端要等缓冲区满或连接关闭才收得到，流式体验
  变成假的
- 响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、
  `Connection: keep-alive`
- 监听 `c.Request.Context().Done()`：客户端断开时这个 ctx 会被取消，
  处理循环要能感知到并停止继续往一个没人听的连接写数据
- 本地直连、无反向代理，不需要处理 Nginx 之类中间层的缓冲配置
