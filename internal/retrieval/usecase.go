// Package retrieval 拥有向量检索 + document_chunks 的读写。
//
// 【embed 在这里做，不在 knowledge 做】代码架构设计 §2.1 的依赖表：
// `retrieval -> domain llm platform`——它是唯一同时持有 llm.Registry
// 和 document_chunks 写权限的包，所以"文本变向量再落库"这件事天然属于它。
// knowledge 只交出切好的 []domain.Chunk（只有 Content，没有向量），
// 这个包负责把每一段变成向量、配上是哪个模型算出来的，再交给 Repo 落库。
package retrieval

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// defaultTopK 是 Search 在调用方没指定 TopK 时用的默认值。
const defaultTopK = 5

// embedBatchSize 是一次 embedding 请求里最多送多少条文本。
//
// 【为什么必须分批】OpenAI 的 /v1/embeddings 对 input 数组有条数上限
// （2048），而 eino 的 embedder 是直通——调用方给多少条它就一次发多少条，
// 整条链上没有别人会替你切。一份切出几千个分块的文档因此会稳定地 400：
// 事务回滚、文档标 failed，重传还是同样的块数、同样失败。
//
// 取 256 而不是贴着 2048：一批的 token 总量也远低于单条 8192 的上限，
// 同时请求数不至于多到把本地 CPU embedding 服务打满。
const embedBatchSize = 256

// Usecase 同时实现 knowledge.ChunkIndexer（IndexDocument/DeleteByDocument）
// 和 conversation.ChunkSearcher（Search）——这就是代码架构设计 §1.2
// 规则 B 说的"一个实现自动满足多个消费者的 port，不需要写适配器"。
//
// 【db 字段只给 Search 用】IndexDocument/DeleteByDocument 的 q 由调用方
// 传入（它们要参与 knowledge.Usecase 那边的事务），但 ChunkSearcher 接口
// （代码架构设计 §5.8）定的签名是 `Search(ctx, req) (...)`，没有 q 参数——
// 检索是纯读操作，从不需要参与调用方的事务（conversation.Usecase.Send
// 不会把"检索"和"写消息"绑进同一个事务：检索失败不该导致消息也写不进去）。
// 所以 Search 走自己持有的连接池，其余两个方法继续走调用方传入的 q。
type Usecase struct {
	repo     Repo
	registry llm.Registry
	llmRepo  llm.ConfigRepo
	db       platform.Querier
	topK     int // 默认值，Search 的 req.TopK > 0 时被覆盖
}

// Option 是技术方案 §4.1 点名的 Functional Options 模式：用在"运行期
// 可能变"的项上。目前只有 topK 一项，必填的依赖（repo/registry/llmRepo/db）
// 留在参数列表里，不塞进 options——那会让编译器帮不上忙（代码架构设计 §6.1）。
type Option func(*Usecase)

func WithTopK(k int) Option {
	return func(u *Usecase) { u.topK = k }
}

func NewUsecase(repo Repo, registry llm.Registry, llmRepo llm.ConfigRepo, db platform.Querier, opts ...Option) *Usecase {
	u := &Usecase{repo: repo, registry: registry, llmRepo: llmRepo, db: db, topK: defaultTopK}
	for _, o := range opts {
		o(u)
	}
	return u
}

// 编译期断言：如果哪一天改坏了方法签名，这里会先于运行时报错。
var _ interface {
	IndexDocument(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) error
	DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error
} = (*Usecase)(nil)

// 【规则 B 的落地】conversation.ChunkSearcher 只有一个方法
// Search(ctx, req domain.SearchRequest) ([]domain.Chunk, error)——
// 签名直接用 domain 类型，所以 *Usecase 自动满足它，不需要适配器。
var _ interface {
	Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error)
} = (*Usecase)(nil)

// Search 实现 conversation.ChunkSearcher（消费方声明的 port，规则 A）。
// 签名直接用 domain.SearchRequest，这正是"规则 B 免掉适配器"的落地——
// *Usecase 因此自动满足 conversation.ChunkSearcher，不需要写转换代码。
func (u *Usecase) Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error) {
	model, err := u.activeEmbeddingModel(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("resolve active embedding model: %w", err)
	}

	embedder, err := u.registry.Embedder(ctx, model.ID.String())
	if err != nil {
		return nil, fmt.Errorf("get embedder for model %s: %w", model.ID, err)
	}

	vecs, err := embedder.Embed(ctx, []string{req.Text})
	if err != nil {
		return nil, fmt.Errorf("embed search query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for 1 query text: %w", len(vecs), platform.ErrUpstream)
	}

	topK := req.TopK
	if topK <= 0 {
		topK = u.topK
	}

	chunks, err := u.repo.Search(ctx, u.db, req.KnowledgeBaseID, vecs[0], model.ModelID, topK)
	if err != nil {
		return nil, fmt.Errorf("search knowledge base %s: %w", req.KnowledgeBaseID, err)
	}
	return chunks, nil
}

// IndexDocument 实现 knowledge.ChunkIndexer：把切好的分块批量 embed，
// 配上模型来源后落库。
//
// 【为什么先 DeleteByDocument】River 的任务可能被重试（进程崩溃、
// 网络抖动导致的暂时性失败），worker 那一侧的处理流程会对同一个文档
// 重新跑一遍这个方法——如果不先清掉上一次可能已经插入的部分分块，
// 重试会在表里留下重复的旧数据。先删后插让这个方法本身对重试是安全的，
// 不需要 River 的重试机制额外做任何特殊处理。
func (u *Usecase) IndexDocument(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) error {
	if err := u.repo.DeleteByDocument(ctx, q, docID); err != nil {
		return fmt.Errorf("clear existing chunks of document %s before reindexing: %w", docID, err)
	}
	if len(chunks) == 0 {
		return nil
	}

	model, err := u.activeEmbeddingModel(ctx, q)
	if err != nil {
		return fmt.Errorf("resolve active embedding model: %w", err)
	}

	embedder, err := u.registry.Embedder(ctx, model.ID.String())
	if err != nil {
		return fmt.Errorf("get embedder for model %s: %w", model.ID, err)
	}

	// 按 embedBatchSize 分批送，再按原顺序拼回一整份——分批只决定
	// "一次请求几条"，不能让向量和 chunks 的下标对应关系错位。
	vecs := make([][]float32, 0, len(chunks))
	for start := 0; start < len(chunks); start += embedBatchSize {
		end := min(start+embedBatchSize, len(chunks))

		batchTexts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			batchTexts = append(batchTexts, c.Content)
		}

		batchVecs, err := embedder.Embed(ctx, batchTexts)
		if err != nil {
			return fmt.Errorf("embed chunks %d-%d of %d in document %s: %w",
				start+1, end, len(chunks), docID, err)
		}
		// 上游返回的向量数量和送进去的文本数量不一致——这不该发生，
		// 但发生的时候必须显式报错，不能假装对齐、悄悄错配某一段的向量。
		if len(batchVecs) != end-start {
			return fmt.Errorf("embedder returned %d vectors for %d chunks: %w",
				len(batchVecs), end-start, platform.ErrUpstream)
		}
		vecs = append(vecs, batchVecs...)
	}

	if err := u.repo.InsertChunks(ctx, q, docID, chunks, vecs, model.ModelID); err != nil {
		return fmt.Errorf("insert %d chunks of document %s: %w", len(chunks), docID, err)
	}
	return nil
}

// DeleteByDocument 实现 knowledge.ChunkIndexer 的另一半。
func (u *Usecase) DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error {
	if err := u.repo.DeleteByDocument(ctx, q, docID); err != nil {
		return fmt.Errorf("delete chunks of document %s: %w", docID, err)
	}
	return nil
}

// activeEmbeddingModel 找出当前配置的 embedding 模型。
//
// "当前"的定义、为什么选"最近创建的" —— 见 llm.LatestByKind 的注释，
// 那才是这条判据唯一的定义处，这里不重复。
func (u *Usecase) activeEmbeddingModel(ctx context.Context, q platform.Querier) (*llm.Model, error) {
	models, err := u.llmRepo.ListModels(ctx, q)
	if err != nil {
		return nil, err
	}
	m := llm.LatestByKind(models, llm.KindEmbedding)
	if m == nil {
		return nil, fmt.Errorf("no embedding model configured yet: %w", platform.ErrNotFound)
	}
	return m, nil
}
