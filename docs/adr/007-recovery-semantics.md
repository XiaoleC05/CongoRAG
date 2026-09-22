# ADR-007：恢复语义——崩溃表与升级时在途的 run

- 状态：已采纳
- 日期：2026-09-22
- 相关：[ADR-006](006-checkpoint-layering.md)、`internal/agent/usecase.go`、
  `internal/agent/model.go`、`migrations/0010_tool_effect_log.up.sql`

## 背景

项目文档 §9.2 第 ③ 步要求在纸上先画一张崩溃表，§9.4 结尾要求定「升级时正在飞的
run 怎么办」。两件事都属「在纸上定完再写代码」那一类：它们的答案是 Resume 的
核心判断逻辑，留给实现随手决定，等于让代码替你定语义，而这类语义一旦落地
就会有数据依赖它。

§9.2 给的原始表格（一个 4 步的 run 在第 3 步崩溃）：

| 情况 | `interrupted` 语义 |
| --- | --- |
| 工具根本没跑 | |
| 工具跑了但结果没落库 | |
| 工具跑了、结果落了库、但没标记第 4 步 | |

要求逐行决定，依据是「Eino checkpoint 能力」与「ToolMetadata」。

## 决策一：三行崩溃表

### 前置事实（决定了这张表能问出什么问题）

1. **Eino 那层不接**（[ADR-006](006-checkpoint-layering.md)）。因此崩溃时，
   编排 runtime **没有留下任何记录**——「工具函数被调用了没有」这件事，
   只能靠业务层自己留下的证据来推断。
2. **业务层的记录粒度是 Step 边界**。工具步骤在**调用之前**就落一行
   `agent_run_steps`（`status = running`，带 `tool_name` 与 `tool_args`），
   返回之后才更新成 `completed` 并把结果写进 `tool_result`。
3. **`tool_effect_log` 在工具返回之后、结果落库之前，用独立事务提交**，键是
   `(step_id, effect_key)`。

### 第 3 条的**顺序**才是这张表能问出问题的原因

两次写入（先账本、后步骤结果）落在不同的独立事务里，所以它们之间有一个
崩溃窗口。这个窗口留下的现场是「**账本有、步骤还是 running**」——
它是一份确凿的证据：**工具确实跑过了**，而结果没有留下来。

反过来写（先步骤结果、后账本）的话，同一个窗口留下的现场是「步骤
completed、账本缺失」，那时恢复根本不看这一步（已完成的步骤不会重放），
而「工具跑了但账本没记」这件事再也无法从数据里看出来。**顺序不能反。**

### 表

| 情况 | 库里长什么样 | `interrupted` 语义 | 恢复动作 |
| --- | --- | --- | --- |
| **工具根本没跑**（或跑了、但两个事务都没提交） | step `running`，无 effect 日志 | 标 `interrupted`；**没有证据说它跑过** | 按 ToolMetadata 决定：`READ_ONLY` / `WRITE_IDEMPOTENT` **重放**；`WRITE_NON_IDEMPOTENT` **拒绝自动恢复** |
| **工具跑了但结果没落库**——账本已提交 | step `running`，**有 effect 日志** | 标 `interrupted`；**有证据说它跑过了** | **一律拒绝**：重放它才是真正的重复执行 |
| **工具跑了、结果落了库、但没标记第 4 步** | step `completed`，有结果 | **不算 `interrupted`**——结果落库就是这一步完成了 | 该步按已完成处理，从「第一个未完成的 Step」继续 |

第二行的两半要分开看：**账本已提交的那一半是可区分的**（这正是第 3 条
顺序的意义），账本也没提交的那一半退化成第一行——那时候没有任何证据，
平台只能按 ToolMetadata 决定，猜错的代价由「拒绝重放有副作用的工具」兜住。

### 第三行为什么不算 `interrupted`

「结果落了库」是一个**已提交的事实**。项目文档 §9.1 要求的是
「**不假装成功**」——这一步不是假装成功，它真的成功了；它缺的只是
`agent_runs.current_step` 这个游标没来得及前进。

把它标成 `interrupted` 会造成真实的损害：恢复时会把一个已经有结果的工具
再跑一遍。所以第三行的判据是**看 step 的状态与结果**，而不是看
`current_step`。

### 拒绝的那条路给用户看到什么

两种拒绝各有一个独立的错误类型（前端按 type 给出不同的下一步提示）：

| 判据 | sentinel / type | 用户看到的 |
| --- | --- | --- |
| 账本说副作用已生效，但结果没留存 | `ErrToolEffectApplied` / `tool_effect_already_applied` | 这一步的副作用可能已经发生，平台不能替你决定重不重放，请重新发起 |
| 工具声明了 `WRITE_NON_IDEMPOTENT`（或 `retry_policy = never`） | `ErrReplayUnsafe` / `replay_unsafe` | 这一步的工具不允许被平台自动重放 |

第二条直接对应项目文档 §9.1 的那句：`WRITE_NON_IDEMPOTENT` 只能靠
`idempotency_key` 保证，**方案 §8 恢复边界明说了不保证它们被自动安全重放**。

### 一个必须记住的副作用：**不自动重放 = 这条 run 不会自己好**

被拒绝的 run 停在 `interrupted`，用户能做的只有重新发起一次运行。
这不是偷懒——「静默重复执行一个已经生效过的副作用」和「用户重发一次」
之间，前者的代价不可逆，后者只是多花一次额度。

## 决策二：升级时正在飞的 run 标 `interrupted`

三选一里挑一个：

| 选项 | 为什么不选 |
| --- | --- |
| 允许旧版本跑完 | 交付形态是**单机单进程**的本地工具（README 的定位），升级就是重启进程——旧进程活不下来，这个选项在物理上不存在 |
| 标 `failed` | 会把它说成服务端故障：`web/src/lib/errors.ts` 的 `failed` 文案指向「检查 API Key 与配额」，错误归因从第一句话起就是错的。而且 `failed` 在我们的状态机里是**不可恢复**的终态 |
| **标 `interrupted`** | 语义准确（「被打断了，可以从断点继续」），且与 `Resume` 入口天然衔接：升级后用户可以对这条 run 发起恢复，新代码按 `state_schema_version` 校验后决定接不接 |

**实现落点**：api 进程启动时做一次扫描，把 `status = running` 的 run 全部
推进 `interrupted`，并给它们那个没有走完的 step 落一行 `interrupted`
（把 ADR 崩溃表的前两行落到数据上）。理由：run 的执行生命周期绑在
**一次活的 HTTP/SSE 请求**上（`StartAgentRun` handler 就在 api 进程里跑），
所以「api 刚启动」这个时刻不可能有真正在飞的 run。

## 后果

- `agent_runs.status` 的 `interrupted` 有了两个明确的写入者：崩溃扫描
  （启动时）与客户端断开（`failRun` 现有的那条路径）。二者语义一致。
- 恢复的入口只接受 `interrupted`：`pending` / `running` 是「还没结束」，
  `completed` / `failed` / `cancelled` 是终态。对终态发起 Resume 返回
  `platform.ErrConflict`。
- 「第一步做完了没有」这个问题，答案来自 `tool_effect_log` 与
  `agent_run_steps.status` 两者，不是 `current_step` 单独一个数。
