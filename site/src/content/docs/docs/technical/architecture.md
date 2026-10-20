---
title: 整体架构
description: 两个进程、八个业务包，以及几条不靠人盯的依赖规则。
---

CongoRAG 是模块化单体，不是微服务。交付形态是两个进程：

- `apps/api`：HTTP 服务，提供 REST、SSE 和 go:embed 进来的前端。
- `apps/worker`：River 的消费端，处理文档和孤儿文件对账。

两个进程共用一个 PostgreSQL。业务表、向量（pgvector）、队列表都在这个库里，所以“写业务记录”和“入队”可以放在一个事务里。

## 业务包

`internal/` 下有八个包：

`domain`、`platform`、`llm`、`knowledge`、`retrieval`、`ctxmgr`、`conversation`、`agent`。

依赖方向有几条约定：

- 业务包不 import gin，也不知道 HTTP 状态码。
- 跨包的“我需要什么”由消费方声明成 port，实现方零 import 边。
- 两个进程各有自己的装配根，重复十几行是正常的，不造共享装配包。

这些约定写在代码注释和 [ADR](/docs/technical/architecture/#设计记录) 里。改代码时不要绕过去，否则编译期能过的边界会慢慢消失。

## 契约

`contracts/openapi.yaml` 是唯一真相。Go 的接口和 TS 的类型都从它生成：

- Go：oapi-codegen，产物 `apps/api/internal/api/generated.go`
- TS：openapi-typescript，产物 `packages/api-client/src/schema.d.ts`

契约的 `info.version` 跟产品版本走，当前是 5.0.2。兼容边界是路径里的 `/api/v1`，不是 `info.version`。

## 数据流

写入侧是异步的：上传 → 落盘 → 入队 → worker 解析/切分/向量化/写索引。

读取侧是同步的：提问 → 检索 → 组装上下文 → 调模型 → SSE 下发。

两条链路共用同一个数据库，但不在同一个事务里。检索失败不会导致消息写不进去；文档处理失败也不会把已经写好的会话弄坏。

## 设计记录

技术取舍写在仓库的 ADR 里：

- [ADR-001 模块化单体](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/001-modular-monolith.md)
- [ADR-002 契约版本](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/002-contract-versioning.md)
- [ADR-003 两套迁移系统](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/003-two-migration-systems.md)
- [ADR-004 halfvec](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/004-halfvec-embeddings.md)
- [ADR-005 SSE over fetch](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/005-sse-over-fetch.md)
- [ADR-006 checkpoint 分层](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/006-checkpoint-layering.md)
- [ADR-007 恢复语义](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/007-recovery-semantics.md)
- [ADR-008 幂等重放窗口](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/008-idempotency-replay-window.md)

## 它不是什么

- 不是微服务：没有服务间 RPC，没有消息中间件。
- 不是无状态服务：文档在磁盘，向量和业务数据在 PostgreSQL。
- 不是多租户：一个实例一组数据，没有账号和权限层。
