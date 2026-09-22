# ADR-006：checkpoint 只做业务层，不接 Eino 的 CheckPointStore

- 状态：已采纳
- 日期：2026-09-22
- 相关：`internal/agent/port.go`、`internal/agent/eino_adk.go`、
  `internal/agent/usecase.go`、`migrations/0005_agents.up.sql`、
  [ADR-007](007-recovery-semantics.md)

## 背景

Eino 自己有 checkpoint 能力。`go.mod` 锁的是 `github.com/cloudwego/eino v0.9.19`，
它的 ADK 里这几样都存在（逐一核对过 `adk` 包的实际源码，不是照文档抄的）：

- `adk.RunnerConfig.CheckPointStore` 字段
- `adk.CheckPointStore` / `adk.CheckPointDeleter` 两个类型别名
- `(*TypedRunner[M]).Resume` / `ResumeWithParams` 两个恢复入口
- `adk.WithCheckPointID(id)` 这个 `AgentRunOption`

也就是说「接上」在 API 层面是可行的。问题在于**它恢复的是什么、什么时候写**。

项目文档 §8 已经把分层写清了：Eino 那层恢复的是**编排 runtime** 状态，
本项目的 checkpoint 恢复的是**业务 Run/Step**（哪一步做完了、哪一步在飞、
工具能不能重放）。§9.2 剩下的那一件要定的事就是：**这两层要不要都接上**，
并且明确警告「不定就会写出两套都半残的恢复」。

## 决策

**不接 Eino 那一层。** 业务层（`internal/agent` 的 `CheckpointStore`）是恢复的
唯一权威。`runAgent` 构造 `adk.RunnerConfig` 时不传 `CheckPointStore`，
因此 `runner.Resume` 在本仓库里没有任何调用点。

`internal/agent` 自己的 `CheckpointStore` 接口保留原样（`Save` / `Load`），
它落的是 `agent_runs.state_snapshot` + `current_step`，粒度 = Step 边界。

## 理由

### 一、Eino 的 checkpoint 只在「中断点」写，崩溃一个字节都不会落

`v0.9.19` 的 `adk` 包里，`runnerSaveCheckPointImpl`（真正调 `store.Set` 的那个
函数）全部调用点只有两处，都在 `runner.go` 的 `typedRunnerHandleIterImpl` 里：

| 位置 | 触发条件 |
| --- | --- |
| `runner.go` 收到 `event.Err` 且 `errors.As` 成 `*CancelError` 时 | `cancelErr.interruptSignal != nil` **且** `checkPointID != nil` |
| `runner.go` 收到 `event.Action.internalInterrupted != nil` 时 | `checkPointID != nil` |

两处都要求一个 **`core.InterruptSignal`**。InterruptSignal 是「工具主动请求
停下来、等人确认」那套人机协同流程产生的信号。而 `docker kill -s KILL`
（issue #62 的崩溃探针）、以及普通的 `context` 取消，都**不会**产生它。

结论是可证伪的一句话：**进程被 KILL 的那一刻，Eino 一个 checkpoint 字节都没写过。**
接上它，对 M4-C 要处理的场景贡献为零。

### 二、它的载荷是 gob 编码的内部结构，且上游自己承认跨版本不稳定

`runnerSaveCheckPointImpl` 写下去的是：

```go
gob.NewEncoder(buf).Encode(&serialization{
    RunCtx:                    runCtx,
    Info:                      info,
    InfoDataSourceInterruptID: infoDataStateID,
    InterruptID2Address:       id2Addr,
    InterruptID2State:         id2State,
    EnableStreaming:           enableStreaming,
})
```

`runContext` / `InterruptID2State` / `InterruptID2Address` 都是 ADK 的内部
编排结构，不是本项目定义的任何东西。

更能说明问题的是同一份源码里那个 `preprocessADKCheckpoint` 函数：它存在的
唯一目的是**修 gob 的跨版本不兼容**——v0.8.0–v0.8.3 的 checkpoint 把
`*State` 注册成 `_eino_adk_react_state` 并实现了 `GobEncode`，v0.7 用同一个名字
注册成普通 struct，gob 认为两种线格式不兼容，于是上游只能在**字节层面**改写
名字前缀（`_eino_adk_state_v080_`，长度必须和原名一样，否则长度前缀对不上）。

这正是我们验收标准里那条要避免的东西：

> 升级后遇到旧格式 checkpoint → **明确拒绝**并提示重新发起

依赖 Eino 那层的话，这条会退化成「gob 解不开」，报错内容由上游内部结构决定，
既不可控也不可测——而且它连「拒绝」和「崩溃」都分不清楚。

### 三、恢复要做的事，Eino 那层根本不知道

恢复的核心判断是**业务语义**：这一步的工具是 `READ_ONLY` 还是
`WRITE_NON_IDEMPOTENT`（`internal/agent/model.go` 的 `SideEffectLevel`）。
Eino 的 runtime 状态里没有这个概念——它不知道 `knowledge_search` 可以重放、
而一个未来会写外部系统的工具不能。

把恢复交给它，等于让一个不了解副作用分级的层来做副作用安全决策。

## 后果

### 好的

- **恢复只有一条路径**，不存在「这条 step 是被业务层跳过的还是被 runtime 层重放的」
  这种查不清的情况。
- **升级后的行为是可控的**：`state_schema_version`（`agent_runs` 的第 8 列）
  是我们自己的 int 常量，比它大就是拒绝恢复并给一句明确提示（见 ADR-007）。
- checkpoint 的形状完全由我们定义，可以按需要加字段而不受上游 gob 注册表约束。

### 代价 / 必须一起记住的

- **拿不到编排层对「这一步走到哪」的记录。** 因为不接那层，崩溃之后
  「工具进没进 `Invoke`」只能靠业务层自己留下的证据（`tool_effect_log`，
  见 ADR-007）来推断——而不是靠 Eino 告诉我们。
- **不保证恢复「正在进行中」的 LLM 流式输出或工具调用现场**（项目文档 §9.3
  本来就明确划在不保证范围内）。恢复的粒度是 Step 边界：一步没走完，
  这一步的输出就丢掉，从这一步重来。
- **`adk.WithCheckPointID` 也不传**。它只在有中断信号的路径上被读，不传它
  没有任何副作用；写在这里是为了防止后来者以为「没传是漏了」。

### 如果将来要重新考虑

出现真正需要「人在环中确认」的场景（比如 `WRITE_NON_IDEMPOTENT` 工具执行前
要用户点确认）时，Eino 这套 interrupt/resume 是对的工具——那时它恢复的是
「等确认的那次中断」，和本 ADR 的业务层恢复**各管各的场景**，
不构成两套半残的恢复。届时应再记一篇 ADR 说明两者的分工，
而不是把本 ADR 改掉。

## 一个具体问题，按本决策回答

> 一个 4 步的 run 在第 3 步崩溃，重启后是谁决定从第 4 步继续？

**业务层**：`internal/agent` 的 Resume 入口（`Usecase.Resume`）。它读
`agent_runs.status` + `current_step` + `agent_run_steps` 里已完成的步骤，
校验 `state_schema_version`，然后从「第一个未完成的 Step」重放。

Eino 在这里不参与任何决策——它连那次崩溃都没记下来（理由一）。
