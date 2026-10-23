---
title: Agent 运行时
description: Agent 怎么跑、步骤怎么落库、取消和恢复的边界在哪。
---

Agent 运行时负责把“模型 + 工具”跑成一条可追踪的 run。它基于 CloudWeGo Eino 的 ADK，但恢复语义由 CongoRAG 自己定义。

## 一次 run 里有什么

- `agent_runs`：一次执行的总体状态、输入、当前步骤。
- `agent_run_steps`：每一步的工具名、参数、结果、状态。
- `run_events`：这条 run 的 SSE 事件，按 run 发号。
- `tool_effect_log`：工具副作用账本，用来判断崩溃后能不能重放。

工具在调用之前就写一行 step（状态 running），返回之后更新成 completed 并写结果。这样崩溃时能看出“这一步开始过没有”。

## 工具与副作用

内置三个工具：知识库检索、会话检索、计算器。工具通过 `GET /api/v1/tools` 暴露。

每个工具声明副作用等级：

- `READ_ONLY`：只读，可以重放。
- `WRITE_IDEMPOTENT`：写，但幂等，可以重放。
- `WRITE_NON_IDEMPOTENT`：写，且不保证幂等，平台不会自动重放。

副作用账本在工具返回之后、步骤结果落库之前，用独立事务提交。这个顺序让“工具跑了但结果没留下来”变成可识别的现场。

## 状态

run 有六个状态：`pending`、`running`、`completed`、`failed`、`cancelled`、`interrupted`。

`completed`、`failed`、`cancelled` 是终态。`interrupted` 可以恢复成 `running`，也可以被用户取消成 `cancelled`。

取消不是失败：已经执行的工具调用留在轨迹里，副作用不回滚，只是不再往下走。

## checkpoint

CongoRAG 不接 Eino 自带的 CheckPointStore。业务层的 checkpoint 是唯一权威，粒度是 Step 边界，落在 `agent_runs.state_snapshot` 和 `current_step`。

原因写在 [ADR-006](https://github.com/XiaoleC05/CongoRAG/blob/main/docs/adr/006-checkpoint-layering.md)：Eino 的 checkpoint 只在中断信号时写，进程被 KILL 时一个字节都不会落；载荷还是 gob 编码的内部结构，跨版本不稳定。

## 没有调度

v5.0.2 只能手动发起 run。没有 cron、webhook、事件触发。要定时跑，在外面写调度，再调 API。

## 幂等

发起 run 接受 `Idempotency-Key`。同一个键重发不会触发第二次执行；服务端把那条 run 已经记录的事件按原 event_id 补发一遍。

幂等和恢复是两件事：幂等是同一请求不重复执行；恢复是崩溃后从断点继续。
