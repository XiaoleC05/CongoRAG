# ADR-005：SSE 帧格式不变，但客户端用 fetch 而不是 EventSource

- 状态：已采纳
- 日期：2026-09-21
- 相关：`docs/sse-protocol.md`、`apps/api/internal/api/sse.go`、
  `web/src/lib/streamChat.ts`

## 背景

流式回答走的是 **SSE 的线路格式**（`text/event-stream`，帧以空行分隔，
`id` / `event` / `data` 三行，`:` 开头是注释行），但**客户端不用浏览器的
`EventSource`**，而是用 `fetch` + `ReadableStream` 自己解析。

这两件事是分开的：线路格式是协议，客户端 API 是实现。`docs/sse-protocol.md`
是帧格式的唯一规范来源，这篇 ADR 只记**为什么客户端不用 `EventSource`**。

## 决策

**服务端按标准 SSE 写帧；客户端用 `fetch` + `ReadableStream` 手工解析。**

## 理由

1. **`EventSource` 只支持 GET。** 发一条消息是 `POST`，携带请求体。
2. **`EventSource` 不能带自定义请求头。** 幂等重放要发 `Idempotency-Key`，
   而认证类头在将来也会需要。
3. **重连策略不可控。** `EventSource` 会自动重连，但重连的时机、退避、
   以及「这次重连还有没有意义」都由它决定；我们要自己的逻辑。

## 后果

- **好处**：请求头、HTTP 方法、重连策略全部在自己手里；协议面仍然是标准
  SSE，`curl -N` 能直接看裸流（调试时这一点很值钱）。
- **代价**：`EventSource` 免费提供的东西全部要自己写——**分帧缓冲**
  （一帧可能跨两次 `read`）、**忽略注释行**（服务端当前并不发心跳，忽略
  `:` 开头的行是 SSE 规范的要求，见 `docs/sse-protocol.md`）、**断线续传**
  （`Last-Event-ID` 请求头不会被浏览器自动带上，要自己记 `lastEventId`
  并拼进 URL）。这些都落在 `web/src/lib/streamChat.ts` 里。
- **必须一起记住的**：**`id` 可以缺席。** 有一条 error 帧是由「事件根本没能
  落库」这个故障本身触发的——那正是分配 `event_id` 的那一步失败了。客户端
  必须容忍它，而且**不能因为收到它就推进续传游标**：那一帧不代表任何一条已
  持久化的事件，拿它当游标会让断线续传从头跳过一批还没收到的事件。
- **如果将来要重新考虑**：如果流式端点改成不需要请求头、也不需要请求体的
  `GET`，`EventSource` 会重新变成一个合理选择。触发条件是「鉴权与幂等都
  不再需要请求头」，而不是「自己写解析器有点烦」。
