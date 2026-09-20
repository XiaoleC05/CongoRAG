// Package agent 拥有 Agent 本体、工具、执行记录（Run/Step）。
//
// 代码架构设计 §5.9/5.10 把工具和 Agent 主循环都放在这一个包里——
// 三个内置工具是本包下的三个文件（tool_calculator.go/
// tool_knowledge_search.go/tool_conversation_search.go），不是子包：
// 子目录就是第 9 个包，而工具的构造函数（NewCalculator 等）在装配根
// 按 agent.NewXxx() 调用，不需要单独的命名空间。
package agent

import (
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
