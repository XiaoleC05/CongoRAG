---
title: CongoRAG 是什么
description: 本地优先的 RAG 问答与 Agent 平台。
---

CongoRAG 是一个在你自己的机器上运行的 RAG 问答与 Agent 平台。

它把文档、向量和模型 API Key 留在本机：文档存在本地目录，向量存在 PostgreSQL 的 pgvector 列里，API Key 用 AES-GCM 加密后写进同一个数据库。模型服务由你提供，CongoRAG 不代管账号，也不转售 token。

它适合这样的场景：你有一些不想上传到第三方 SaaS 的文档，想用 RAG 提问，又希望看到回答引用了哪一段原文；或者你想让模型带着知识库检索、会话检索和计算器这几个工具，跑多步任务，并留下每一步的轨迹。

它现在还不是什么：没有账号系统，没有多租户，没有云端同步，没有内置备份。默认只监听 `127.0.0.1:3210`，不要直接把它暴露到公网。

当前版本是 v5.0.2。项目使用 MIT 许可证，源码在 [GitHub](https://github.com/XiaoleC05/CongoRAG)。
