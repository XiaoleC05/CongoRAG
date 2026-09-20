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
- 心跳：服务端每 15 秒发一条注释行 `: heartbeat\n\n`（以 `:` 开头的行是 SSE 规范里的注释，客户端应当忽略，只用来防止连接被中间设备当成空闲连接掐断）

**唯一的例外：`id` 可以缺席。** 有一条 error 帧是由"事件根本没能落库"这个
故障本身触发的——那正是分配 `event_id` 的那一步失败了（见
`apps/api/internal/api/sse.go` 的写失败兜底分支）。它照 `event:` / `data:`
两行发出去，没有 `id`。**客户端必须容忍这一点，而且不能因为收到它就推进
续传游标**：这一帧不代表任何一条已持久化的事件，拿它当游标会让断线续传
从头跳过一批还没收到的事件。判据很简单——`id` 缺席的帧不参与发号，只用于
把失败告诉用户。

**一帧可能跨两次 `read()`**——TCP 不保证一次 `read` 刚好读到一个完整帧的边界。
解析器必须维护一个缓冲区，按 `\n\n` 切出完整帧，切不出来的部分留到下一次
`read()` 再拼。开发文档已经点过这个坑：先 `curl -N` 看裸流，再写解析代码。

## 事件类型

```text
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
④ 服务端从 conversation_events 表按 event_id > after_event_id 查出补发的部分，
   发完历史再继续实时推送新产生的事件
```

**`Last-Event-ID` 请求头不会被浏览器自动带上**——那是 `EventSource` 的行为，
`fetch` 什么都不会替你做。重连循环、记录 `lastEventId`、拼进请求 URL，
三件事都是前端自己的代码要做的（见 `web/src/lib/streamChat.ts`）。

## 幂等与断线的闭环（M4-B 计划，当前未实现）

**下面写的是设计意图，v1.0 的代码里没有实现，不要按它写客户端。**
服务端不读 `Idempotency-Key` 请求头（`contracts/openapi.yaml` 也没声明它），
也没有任何语句往 `idempotency_keys` 表（`migrations/0003_conversations.up.sql`
建的）里写。所以带同一个键重发一次请求**不会**重放：`Send` 照常分配一个新的
`sequence_no`，会话里多出一轮用户消息 + 一份 assistant 回答，模型调用和
BYOK 的 key 各多扣一次。

计划中的语义：`POST /conversations/{id}/messages` 带 `Idempotency-Key` 请求头，
命中重复键（同一个 `(endpoint, idempotency_key)` 组合）时服务端**不重新执行**
一遍生成，而是返回已创建的资源标识（这里是那条 assistant 消息所在会话的信息），
客户端据此调 `GET .../events?after_event_id=0` 重新订阅，走上面那条续传路径
接着看到剩下的内容。

落地时的位置：在 messages handler 里读这个头，把
`(endpoint, idempotency_key, resource_id)` 和 sequence 分配放进**同一个事务**
插入；`internal/platform/pgerr.go` 已经把 `idempotency_keys` 的唯一约束违规
（23505）翻译成 `platform.ErrIdempotentHit`，但目前没有语句能撞上那条约束，
所以它是条死路径。

## 服务端实现要点

- 每条事件写出后立刻 `Flush()`——不 flush 的话事件会攒在 Go 的
  buffered writer 里，客户端要等缓冲区满或连接关闭才收得到，流式体验
  变成假的
- 响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、
  `Connection: keep-alive`
- 监听 `c.Request.Context().Done()`：客户端断开时这个 ctx 会被取消，
  处理循环要能感知到并停止继续往一个没人听的连接写数据
- 本地直连、无反向代理，不需要处理 Nginx 之类中间层的缓冲配置
