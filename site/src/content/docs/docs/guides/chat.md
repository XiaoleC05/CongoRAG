---
title: 对话与引用
description: 发消息、看流式回答、断线续传和幂等重发。
---

对话是一条会话里的消息序列。发消息的响应本身就是 SSE 流，不是先返回一个任务 id 再轮询。

## 发一条消息

`POST /api/v1/conversations/{id}/messages`，请求体里放 `text`。文本上限是 8000 字符，超限返回 `invalid_argument`，不会写消息行。

服务端会：

1. 用当前 embedding 模型检索这个会话绑定的知识库。
2. 按 token 预算组装上下文；超预算的历史交给模型压缩。
3. 调用 chat 模型，逐 token 下发。
4. 把 citation 随流推送，不是等回答结束再一次性给。

## 流里的帧

常见事件：

| event | 含义 |
| --- | --- |
| `token` | 一段文本增量 |
| `citation` | 命中的一个分块：chunkId、documentId、filename、snippet、score |
| `error` | 出错，data 里有 type 和 detail |
| `done` | 正常结束 |

每条事件带 `id`，是这条会话里单调递增的 event_id。它可能有空洞，不要假设连续。

## 断线之后

客户端记住最后一个 event_id，重连时请求：

```text
GET /api/v1/conversations/{id}/events?after_event_id=123
```

服务端把 `event_id > 123` 的事件按顺序补发一遍，发完就结束响应，不会继续持有连接等新事件。

这也是为什么前端不用浏览器原生的 `EventSource`：它只支持 GET、不能带自定义请求头，断线策略也不可控。CongoRAG 的客户端用 `fetch` + `ReadableStream` 自己解析。

## 重复提交

发消息接受可选的 `Idempotency-Key` 请求头。同一个会话带同一个键重复提交时，服务端不会重新生成，而是把那一轮已经记录的事件按原来的 event_id 补发一遍。

适合网络超时后重试：你不知道上一次有没有成功，带同一个键重发即可，不会得到两条回答。

## 上下文

上下文按 token 预算组装。超出预算的历史会被压缩成摘要；摘要也在数据库里，和原始消息分开存。

预算和压缩策略不是无限可调的。上下文太长时，模型仍然可能丢细节；重要信息最好放在知识库里，而不是只放在对话历史里。

## 分页

会话消息、知识库文档、Agent 运行历史都用 keyset 游标分页。请求带 `limit` 和 `cursor`，响应是 `{items, nextCursor}`。`nextCursor` 为 `null` 表示到底了。

游标是不透明的，不要解析它。
