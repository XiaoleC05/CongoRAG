---
title: API
description: 端点总览、错误格式、分页和兼容边界。
---

API 的契约在 `contracts/openapi.yaml`，以那份文件为准。下面列出端点总览。

- 契约文件：<https://github.com/XiaoleC05/CongoRAG/blob/main/contracts/openapi.yaml>
- 兼容边界：路径里的 `/api/v1`
- 错误响应：`application/problem+json`

## 认证

没有认证。默认只监听 `127.0.0.1:3210`。不要把 API 直接暴露到公网。

## 端点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活检查，不碰数据库 |
| GET | `/readyz` | 就绪检查，探数据库，不可用时 503 |
| GET / POST | `/api/v1/knowledge-bases` | 列表 / 新建 |
| GET / PATCH / DELETE | `/api/v1/knowledge-bases/{id}` | 详情 / 改名 / 删除 |
| GET / POST | `/api/v1/knowledge-bases/{id}/documents` | 文档列表（分页）/ 上传 |
| POST | `/api/v1/knowledge-bases/{id}/reindex` | 重建整个知识库 |
| POST | `/api/v1/knowledge-bases/{id}/search` | 检索调试 |
| GET / DELETE | `/api/v1/documents/{id}` | 文档详情 / 删除 |
| POST | `/api/v1/documents/{id}/reindex` | 重建单份文档 |
| GET / POST | `/api/v1/conversations` | 会话列表（分页）/ 新建 |
| GET / POST | `/api/v1/conversations/{id}/messages` | 消息列表 / 发消息 |
| GET | `/api/v1/conversations/{id}/events` | 补发 after_event_id 之后的事件 |
| GET / POST | `/api/v1/providers` | 模型服务配置 |
| GET | `/api/v1/usage` | 按模型聚合的 token 用量 |
| GET | `/api/v1/tools` | 工具目录 |
| GET / POST | `/api/v1/agents` | Agent 列表 / 新建 |
| GET | `/api/v1/agents/{id}` | Agent 详情 |
| GET / POST | `/api/v1/agents/{id}/runs` | 运行列表（分页）/ 发起运行 |
| GET | `/api/v1/runs/{runId}/events` | 补发 run 的事件 |
| POST | `/api/v1/runs/{runId}/cancel` | 取消运行 |
| POST | `/api/v1/runs/{runId}/resume` | 从断点恢复 |
| GET | `/api/v1/runs/{runId}/steps` | 运行轨迹 |

## 幂等

这两个端点接受 `Idempotency-Key` 请求头：

- `POST /api/v1/conversations/{id}/messages`
- `POST /api/v1/agents/{id}/runs`

同键重发不会重新执行，服务端按原来的 event_id 补发事件。

## 分页

会话消息、知识库文档、Agent 运行历史用 keyset 游标分页：

- 请求带 `limit` 和 `cursor`
- 响应是 `{items, nextCursor}`
- `nextCursor` 为 `null` 表示到底

游标是不透明的，不要解析。

## 错误

错误响应是 `application/problem+json`，形状见契约里的 `Problem`。SSE 的错误帧用同一套 `type` 枚举，客户端可以复用一份文案映射。

5xx 不返回内部错误原文。排查时看服务端日志，不要指望 API 把 SQLSTATE 或连接串带出来。

## 请求体上限

- JSON 端点：1 MiB
- 文档上传：`CONGORAG_MAX_UPLOAD_BYTES`，默认 32 MiB
- 聊天/Agent 输入：8000 字符
