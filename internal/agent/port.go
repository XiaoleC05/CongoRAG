package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// Tool 是一个可以被 Agent 调用的工具。
//
// 【为什么不是 Eino 的 tool.BaseTool】那个接口服务的是"喂给 Eino 的
// ChatModelAgent"这一件事,不携带 Metadata()（副作用等级/重试策略）——
// 这些信息是本项目自己的 M4-C 恢复逻辑需要的,Eino 不认识。
// eino_adk.go 里的适配器把这个接口包成 Eino 认识的形状,反过来的转换
// （Eino → 本接口）从不需要发生。
type Tool interface {
	Name() string
	Description() string
	Metadata() Metadata
	Spec() ToolSpec
	Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// Registry 是工具的注册表——技术方案 §4.1 的注册表模式,工具增删不改
// Agent 主循环。
type Registry interface {
	Register(t Tool)
	Get(name string) (Tool, error)
	List() []Tool
}

// Repo 是 agents/agent_runs/agent_run_steps 三张表的存取接口。
type Repo interface {
	CreateAgent(ctx context.Context, q platform.Querier, a *Agent) error
	GetAgent(ctx context.Context, q platform.Querier, id uuid.UUID) (*Agent, error)
	ListAgents(ctx context.Context, q platform.Querier) ([]*Agent, error)

	InsertRun(ctx context.Context, q platform.Querier, r *Run) error
	GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*Run, error)
	ListRunsByAgent(ctx context.Context, q platform.Querier, agentID uuid.UUID) ([]*Run, error)

	// UpdateRunStatus 用 CAS（WHERE status = from）——和
	// knowledge.PgDocRepo.UpdateStatus 同样的模式,防止并发写入把一个
	// 已经终态的 Run 又改回中间态。
	UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to RunStatus) error

	InsertStep(ctx context.Context, q platform.Querier, s *Step) error
	StepsByRun(ctx context.Context, q platform.Querier, runID uuid.UUID) ([]*Step, error)

	// ListToolCatalog 读 tools 表的种子数据,给创建 Agent 的表单渲染
	// 勾选列表用——它是纯只读目录查询,和 Registry（真正能被调用的工具
	// 实现）是两件独立的事：目录可能列出比 Registry 里实际注册的更多
	// 或更少的名字（比如种子数据加了新工具但代码还没实现完），两者故意
	// 不强制同步,由部署时的纪律保证一致,不是代码层面的约束。
	ListToolCatalog(ctx context.Context, q platform.Querier) ([]ToolCatalogEntry, error)
}

// ToolCatalogEntry 是 tools 表一行的只读投影。
type ToolCatalogEntry struct {
	Name            string
	Description     string
	SideEffectLevel SideEffectLevel
}

// KnowledgeSearcher 是 tool_knowledge_search.go 声明的唯一跨包 port
// （规则 A）。由 retrieval.Usecase 实现——签名用 domain.SearchRequest/
// domain.Chunk 这两个中性类型，自动满足，不需要适配器（规则 B）。
// 和 conversation.ChunkSearcher 是同一个方法签名，两个包各自declare
// 自己的 port 而不是共用一个 interface 类型，是"消费方声明"这条规则的
// 直接体现——即使碰巧长得一样。
type KnowledgeSearcher interface {
	Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error)
}

// ConversationSearcher 是 tool_conversation_search.go 声明的唯一跨包
// port。由 conversation.Usecase.SearchMessages 实现。
type ConversationSearcher interface {
	SearchMessages(ctx context.Context, convID uuid.UUID, query string) ([]domain.MessageSnippet, error)
}

// CheckpointStore 落 Step 边界的快照——这一轮只有 Save 真正被
// Usecase.Start 调用（每步执行完存一次 current_step + state_snapshot，
// 让 GET /agents/{id}/runs/{runId} 在执行中途也能看到准确进度）。
//
// 【Load 这一轮没有调用点,但不是半成品】它是一个完整、正确、可独立
// 测试的只读方法——只是 M4-C 的 Resume 入口（会调用 Load 决定从哪个
// Step 继续）现在还不存在。等 M4-C 开工时,Resume 直接调用这个已经在
// 这里的方法,不需要回头改 CheckpointStore 的接口形状。
type CheckpointStore interface {
	Save(ctx context.Context, q platform.Querier, runID uuid.UUID, step int, output string, snap json.RawMessage) error
	Load(ctx context.Context, q platform.Querier, runID uuid.UUID) (*Run, error)
}
