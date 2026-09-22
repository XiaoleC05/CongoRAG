// Package agent 拥有 Agent 本体、工具、执行记录（Run/Step）。
//
// 代码架构设计 §5.9/5.10 把工具和 Agent 主循环都放在这一个包里——
// 三个内置工具是本包下的三个文件（tool_calculator.go/
// tool_knowledge_search.go/tool_conversation_search.go），不是子包：
// 子目录就是第 9 个包，而工具的构造函数（NewCalculator 等）在装配根
// 按 agent.NewXxx() 调用，不需要单独的命名空间。
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// SideEffectLevel 标记一个工具的副作用等级——M4-C 恢复逻辑靠它判断
// "崩溃后重放这一步安不安全"（开发文档 §8.C）：ReadOnly/WriteIdempotent
// 可以直接重放，WriteNonIdempotent 必须带 idempotency_key 才允许。
// 这一轮三个工具全是 ReadOnly，所以真正的重放判定逻辑本身留给 M4-C，
// 这里只把分类信息定义出来、让工具能声明自己属于哪一类。
type SideEffectLevel string

const (
	ReadOnly           SideEffectLevel = "READ_ONLY"
	WriteIdempotent    SideEffectLevel = "WRITE_IDEMPOTENT"
	WriteNonIdempotent SideEffectLevel = "WRITE_NON_IDEMPOTENT"
)

// RetryPolicy 同样是 M4-C 恢复逻辑的判据，这一轮先把分类定义出来。
type RetryPolicy string

const (
	RetryNever            RetryPolicy = "never"
	RetrySafe             RetryPolicy = "safe"
	RetryNeedsIdempotency RetryPolicy = "needs_idempotency_key"
)

// Metadata 描述一个工具的重放/重试属性。
type Metadata struct {
	SideEffectLevel SideEffectLevel
	RetryPolicy     RetryPolicy
}

// ToolSpec 是喂给模型的工具描述——和 llm 包的线路类型分开定义，
// 因为它服务的是 Eino 的 tool.BaseTool 适配（eino_adk.go），
// 不是 llm.ChatModel 的调用路径，没有理由让 agent 包依赖 llm 包
// 来表达"一个工具长什么样"这件和 LLM 调用协议无关的信息。
type ToolSpec struct {
	Name        string
	Description string
	// Schema 是标准 JSON Schema 文档（application/schema+json），
	// 描述 Invoke 的 args 参数长什么样。eino_adk.go 里的适配器把它
	// 解析成 Eino 认识的 schema.ParamsOneOf。
	Schema json.RawMessage
}

// Agent 是用户创建的一个 Agent 配置。
type Agent struct {
	ID          uuid.UUID
	Name        string
	Description string
	Instruction string
	// ToolNames 是这个 Agent 被允许使用的工具名子集，对应
	// migrations/0005_agents.up.sql 的 tools 表种子数据里的 name 列。
	ToolNames []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RunStatus 是一次 Agent 执行的六态。
//
// 【方案没有定义这个枚举,是这里补的】代码架构设计 §5.10 原话：
// 方案 §8 给的枚举（pending|running|completed|failed|interrupted）
// 是 agent_run_steps 的，agent_runs 的状态集方案从来没定义过。
// Cancelled 是本项目自己加的一态——取消这个动作发生在 run 维度
// （见 StepStatus 的注释），所以只有 Run 有这一态，Step 没有。
type RunStatus string

const (
	RunPending     RunStatus = "pending"
	RunRunning     RunStatus = "running"
	RunCompleted   RunStatus = "completed"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunInterrupted RunStatus = "interrupted"
)

// runTransitions 是 agent_runs 的状态迁移表（issue #59）。
//
// 方案 §4.1 要求「非法迁移显式拒绝」，但它给的只是状态**枚举**——表本身
// 要自己画。行 = 状态，格 = 允许迁到哪几个状态；不在格子里的就是拒绝。
//
// 【每一行为什么这样定】
//
//   - pending → running / cancelled / failed：pending 是数据库列默认值，
//     正常路径不会停在它上面（InsertRun 直接写 running），但一条手工插入
//     或将来某个"排队等待调度"的实现会用到这三条边。
//   - running → 四个终态 + interrupted：正常结束、失败、用户取消、
//     进程崩溃（启动扫描把在途的 run 推进 interrupted，见 ADR-007）。
//   - interrupted → running / cancelled / failed：running 这条边就是 **Resume**
//     （崩溃后从断点继续）；cancelled 是"用户放弃这条没跑完的运行"。
//   - completed / failed / cancelled 是**终态**，没有任何出边。
//
// 【为什么 failed → running 被拒绝，而不是允许"重试一次失败的 run"】
// 这条边是 ADR-007 的直接编码：恢复入口只接受 interrupted。failed 意味着
// 这次运行**真的失败了**（上游模型报错、参数不合法之类），重放一遍只会
// 以同样的方式再失败一次，而它消耗的是用户自己配的额度。
// 把它挡在状态机这一层，比在 Resume 里写一个 if 更难被绕过。
//
// 【为什么 completed → running 必须被拒绝】它是方案 §8.4 点名的三行样例
// 之一。允许它的话，resume 就无法判断"这一步到底做完了没有"——而整个恢复
// 逻辑建立在这个判断上。
var runTransitions = map[RunStatus][]RunStatus{
	RunPending:     {RunRunning, RunCancelled, RunFailed},
	RunRunning:     {RunCompleted, RunFailed, RunCancelled, RunInterrupted},
	RunInterrupted: {RunRunning, RunCancelled, RunFailed},
	RunCompleted:   {},
	RunFailed:      {},
	RunCancelled:   {},
}

// CanTransition 判断从 s 状态迁到 to 状态是否合法。
//
// 【未知状态返回 false】迁移表里查不到就当成"不允许"，而不是"随便迁"。
// 这样一条写错了的状态字符串（比如 "Succeeded"）会被挡下来并报冲突，
// 而不是静默地把 run 推进一个没人认识的状态。
func (s RunStatus) CanTransition(to RunStatus) bool {
	for _, ok := range runTransitions[s] {
		if ok == to {
			return true
		}
	}
	return false
}

// IsTerminal 判断这个状态是不是终态（没有任何出边）。
//
// Resume 入口用它做第一道判断：终态的 run 没有可恢复的东西。
func (s RunStatus) IsTerminal() bool {
	return len(runTransitions[s]) == 0
}

// StepStatus 是一个执行步骤的五态,方案 §8 定义的那个枚举。
type StepStatus string

const (
	StepPending     StepStatus = "pending"
	StepRunning     StepStatus = "running"
	StepCompleted   StepStatus = "completed"
	StepFailed      StepStatus = "failed"
	StepInterrupted StepStatus = "interrupted"
)

// Run 是一次 Agent 执行的记录。
//
// 【没有 MessageID 字段】见 migrations/0005_agents.up.sql 的注释：
// 这一轮的 Agent 执行独立于 conversation 包,不从一条会话消息触发,
// 没有对应的消息可挂。
type Run struct {
	ID                 uuid.UUID
	AgentID            uuid.UUID
	Status             RunStatus
	CurrentStep        int
	Input              string
	Output             string
	StateSnapshot      json.RawMessage
	StateSchemaVersion int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Step 是一次执行里的一步——一次模型生成,或者一次工具调用。
type Step struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	Seq        int
	Type       string // llm | tool
	Status     StepStatus
	ToolName   string
	ToolArgs   json.RawMessage
	ToolResult json.RawMessage
	TokenUsage *domain.TokenUsage
	LatencyMS  int
	Error      string
	CreatedAt  time.Time
}

const (
	StepTypeLLM  = "llm"
	StepTypeTool = "tool"
)

// RunEvent 是一条已经持久化的 run 级事件。
//
// 形状照 conversation.Event 来（ID 是 run 内单调递增的号，Type 与 Payload
// 里的 "type" 字段重复但省得每个消费方都解一次 JSON）。两张事件表并存、
// 各自编号，理由见 migrations/0009_run_events.up.sql。
type RunEvent struct {
	ID      int64
	Type    string
	Payload json.RawMessage
}

// ToolEffect 是 tool_effect_log 的一行（issue #63）。
//
// 它记录的是「这一步的工具效果已经被施加过了」。恢复路径先查它再决定
// 重放与否——判读方向见 migrations/0010_tool_effect_log.up.sql 的注释。
type ToolEffect struct {
	StepID    uuid.UUID
	EffectKey string
}

// CurrentStateSchemaVersion 是当前代码写出的 checkpoint 快照的结构版本。
//
// 【用途】任何**破坏性**的快照结构变更就把它 +1。Resume 入口拿
// agent_runs.state_schema_version 跟它比，不匹配就拒绝恢复并提示重新发起
// （项目文档 §9.4 / issue #65）。
//
// 【为什么是 int 而不是给快照加一个 "version" 字段】列已经存在
// （migrations/0005_agents.up.sql:58），而且列在行上、不在 JSON 里——
// 读它不需要先把 jsonb 解出来。快照结构正好坏掉时，正是解 jsonb 这一步
// 会出问题，所以版本号必须能**在不解析快照的前提下**读到。
//
// 【为什么现在是 1】StateSnapshot 至今写的是 NULL（见 usecase.go 的
// checkpoint 注释：真实快照结构留给 M4-C，不提前猜一个序列化格式）。
// 结构没变过，所以版本没动过。真加结构时改这个常量 + 迁移里回填，
// 两件事必须一起做。
const CurrentStateSchemaVersion = 1

// EffectKey 计算一步的工具效果指纹。
//
// 【为什么要绑参数而不只是工具名】同一个工具用不同参数调用是两次不同的
// 副作用。只用工具名的话，「先查 A 再查 B」这两步会被算成同一个效果，
// 恢复时第二步会被误判成"已经做过了"而跳过——查 B 的结果永远不会出现。
//
// 【为什么用 sha256 而不是把原始参数当键】参数是任意 JSON（可能很长、
// 可能含换行），塞进一个 text 列做唯一约束既费空间又不好读。摘要的碰撞
// 概率在这个规模下可以忽略，而且它**是确定性的**——同样的参数永远得到
// 同样的键，这正是恢复判据需要的东西（不能用随机数或时间戳）。
//
// 【为什么不带 run_id / step_id】它们已经是行的另一部分（表的主键是
// (step_id, effect_key)），放进摘要只会让键更难读，且不增加区分度。
//
// ══════════════════════════════════════════════════════════════════
// 【为什么必须先规范化，这一步不能省】
//
// 同一个效果会被算**两次**：执行工具时（参数来自 Eino 的事件，是原始字节）
// 和恢复时（参数从 `agent_run_steps.tool_args` 读回来，而那一列是 **jsonb**
// ——Postgres 会重写它：冒号与逗号后加空格、键按长度重排）。
//
//    写入 {"a":1,"b":2,"operator":"+"}
//    读回 {"a": 1, "b": 2, "operator": "+"}
//
// 两者直接哈希必然不同，于是恢复时 `ToolEffectApplied` **永远返回 false**，
// 那一步被排进重放列表、工具真的被执行第二次，而且因为两边算出的键不同，
// 唯一约束也不会拦——**全程无错误、无日志**。这正是这套机制存在的理由
// （"一个会静默重复执行的 resume，比没有 resume 更糟"）被反过来打脸的情形。
//
// 所以先解析再重新序列化：`json.Marshal` 对 map 按键排序、输出紧凑格式，
// 两侧因此得到同一份字节。数字走 float64 会损失末尾精度，但**两侧损失得
// 一模一样**，判据要的是稳定，不是精确。
//
// 【解析不了怎么办】退回原始字节。参数本来就不是合法 JSON 的话，两次
// 读到的也都是同一份原始字节（jsonb 存不下非法 JSON，走不到这条路径）。
//
// ══════════════════════════════════════════════════════════════════
func EffectKey(toolName string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0}) // 分隔符：避免 ("ab","c") 和 ("a","bc") 撞到同一个摘要
	h.Write(canonicalJSON(args))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON 把一份 JSON 变成与 Postgres jsonb 往返无关的规范形式。
//
// 【为什么是"解析再重新序列化"而不是"删掉空白"】jsonb 还会**重排键**
// （按长度，等长按字节序），而 Go 的 map 序列化按字典序——两者不一致没关系，
// 关键是**两侧都走这一条路**，得到的是同一个函数对同一份数据的输出。
// 手写"删空白"要自己处理字符串字面量里的空格，而那是错的来源。
func canonicalJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
