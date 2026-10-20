---
title: 流式协议
description: SSE 帧、事件 id、断线续传，以及为什么不用 EventSource。
---

对话和 Agent 运行都走 SSE。服务端逐帧写出，客户端用 `fetch` + `ReadableStream` 自己解析。

## 为什么不用 EventSource

浏览器原生的 `EventSource` 只支持 GET，不能带自定义请求头。CongoRAG 需要：

- `Idempotency-Key` 请求头
- 自己控制重连策略
- 自己记录最后一个 event_id

所以客户端不用 `EventSource`。代价是断线重连、游标、缓冲都要自己写。

## 帧

标准 SSE 文本帧，以空行结束：

```text
id: 42
event: token
data: {"text":"你好"}

```

- `id`：这条事件的 event_id，单调递增，可能有空洞。
- `event`：事件类型。
- `data`：一行 JSON。

**当前没有心跳帧。** 客户端仍然要忽略以 `:` 开头的注释行，这是 SSE 解析器本来该做的事。

## 事件

| event | 出现在 | data |
| --- | --- | --- |
| `run_started` | Agent 流 | `{"runId": "..."}`，永远首帧 |
| `token` | 对话、Agent | `{"text": "..."}` |
| `citation` | 对话、Agent | chunkId、documentId、filename、snippet、score |
| `tool_call` | Agent | 工具 id、name、args |
| `tool_result` | Agent | 工具 id、result |
| `error` | 两者 | type、detail |
| `done` | 两者 | `{}` |

`run_started` 只出现在 Agent 运行流上，普通聊天没有 Run，不会发这一帧。

错误帧可能没有 `id`：事件没落库、或者 run 根本没开始。这类帧不推进续传游标，客户端不能拿它当 lastEventId。

## 续传

客户端记住最后一个 event_id，重连时请求：

```text
GET /api/v1/conversations/{id}/events?after_event_id=123
GET /api/v1/runs/{runId}/events?after_event_id=123
```

服务端把 `event_id > 123` 的事件按顺序补发，发完就结束响应，不会继续持有连接等新事件。

对话事件按会话发号，Agent 事件按 run 发号。两张事件表、两套计数器，不合并。

## 一帧可能跨两次 read

TCP 不保证一次 `read` 正好读到一帧。客户端必须维护缓冲区，按 `\n\n` 切帧，切不出来的留到下一次。

这是自己解析 SSE 最容易踩的坑。先用 `curl -N` 看裸流，再写解析代码。

完整规范在仓库的 [docs/sse-protocol.md](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/sse-protocol.md)。
