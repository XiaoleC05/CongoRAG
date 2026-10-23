---
title: CongoRAG 是什么
description: 本地优先的 RAG 问答与 Agent 平台。
---

CongoRAG 运行在你自己的机器上，做 RAG 问答和 Agent 任务。

文档放在本地目录，向量写进 PostgreSQL 的 pgvector 列，模型 API Key 用 AES-GCM 加密后存进同一个数据库。模型服务由你提供；CongoRAG 不代管账号，也不转售 token。

你可能有一批不想上传到第三方 SaaS 的文档，想用 RAG 提问，并且看到回答引用了哪一段原文。也可以让模型带着知识库检索、会话检索和计算器跑多步任务，每一步留下轨迹。

目前没有账号系统、多租户、云端同步和内置备份。默认只监听 `127.0.0.1:3210`；不要直接暴露到公网。

当前版本是 v5.0.2。项目使用 MIT 许可证，源码在 [GitHub](https://github.com/XiaoleC05/CongoRAG)。
