---
title: 数据与存储
description: PostgreSQL 里有什么，磁盘上有什么，以及备份为什么必须成对。
---

CongoRAG 的数据分两处：PostgreSQL 和本地数据目录/数据卷。两处必须一起备份。

## PostgreSQL

业务表大致分五组：

| 组 | 表 | 用途 |
| --- | --- | --- |
| 知识库 | `knowledge_bases`、`documents`、`document_chunks` | 知识库、文档元数据、分块与向量 |
| 模型 | `llm_providers`、`llm_models` | provider 配置、模型名与能力 |
| 对话 | `conversations`、`messages`、`conversation_events`、`conversation_summaries`、`conversation_counters`、`idempotency_keys` | 会话、消息、SSE 事件、摘要、幂等键 |
| Agent | `tools`、`agents`、`agent_runs`、`agent_run_steps`、`run_events`、`run_counters`、`tool_effect_log` | Agent 配置、运行、步骤、事件、副作用账本 |
| 计量 | `token_usage` | 每次模型调用的 token 用量 |

River 自己还有一组 `river_*` 队列表，由 river 的迁移管理，不属于上面任何一组。

两套迁移系统并存：业务表归 golang-migrate，队列表归 river。升级时要分别跑 `make migrate-up` 和 `make river-migrate-up`。

## 磁盘

| 路径 | 内容 |
| --- | --- |
| `CONGORAG_DOCUMENTS_DIR`（默认 `./data/documents`） | 上传的原始文件，文件名是 uuid + 扩展名 |
| `CONGORAG_MASTER_KEY_PATH`（默认 `./data/master.key`） | 主密钥，权限 0600 |
| `CONGORAG_TIKTOKEN_CACHE_DIR`（默认 `./data/tiktoken-cache`） | tiktoken 词表缓存 |

数据库里的 `documents.storage_key` 指向磁盘上的文件，`filename` 是用户看到的原始名字。两者分开，避免同名文件互相覆盖。

## 向量

`document_chunks.embedding` 是 `halfvec`，初始不带维度。引导页探测出 embedding 维度后，迁移/初始化会把列改成 `halfvec(N)`，再建 HNSW 索引。

索引的 opclass 是 `halfvec_cosine_ops`，检索用余弦距离 `<=>`。halfvec 的索引上限是 4000 维；超过这个值，建不出索引。

## 备份

两样一起备：

1. `pg_dump` 出来的数据库。
2. 数据目录/数据卷。

只备数据库，文档记录还在、原始文件没了；只备文件，反过来。主密钥丢了，数据库里加密的 API Key 全部作废。

具体命令见[升级与回滚](/docs/deploy/upgrade/)。
