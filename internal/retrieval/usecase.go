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

// Usecase 同时实现 knowledge.ChunkIndexer（EmbedChunks/ReplaceChunks/DeleteByDocument）
// 和 conversation.ChunkSearcher（Search）——这就是代码架构设计 §1.2
// 规则 B 说的"一个实现自动满足多个消费者的 port，不需要写适配器"。
//
// 【db 字段只给 Search 用】EmbedChunks/ReplaceChunks/DeleteByDocument 的 q
// 由调用方传入——三个方法都作用在调用方指定的那个库/连接上。按
// knowledge.ChunkIndexer 的约定，q 的语义随方法而异（issue #97）：
//   - EmbedChunks 传进来的必须是**连接池而不是事务**（它内部要发 embed
//     的 HTTP 请求，见 EmbedChunks 的注释）；
//   - ReplaceChunks / DeleteByDocument 不碰网络，可以传事务——调用方
//     （knowledge.ProcessDocument）正是把 ReplaceChunks 连同一个状态更新
//     包进事务，来保证"删了旧分块但没插上新的"不留库。
//
// 而 ChunkSearcher 接口（代码架构设计 §5.8）定的签名是
// `Search(ctx, req) (...)`，没有 q 参数——检索是纯读操作，从不需要参与
// 调用方的事务（conversation.Usecase.Send 不会把"检索"和"写消息"绑进
// 同一个事务：检索失败不该导致消息也写不进去）。所以 Search 走自己持有
// 的连接池，其余三个方法继续走调用方传入的 q。
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
// 这三个方法就是 knowledge.ChunkIndexer 的全部——不 import knowledge
// 而在这里重写一遍签名，是"retrieval 不依赖 knowledge"这条依赖边的代价。
var _ interface {
	EmbedChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) ([][]float32, string, error)
	ReplaceChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk, vecs [][]float32, model string) error
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

// EmbedChunks 实现 knowledge.ChunkIndexer 里的"只做网络调用、不碰数据库"
// 那一半：解析出当前生效的 embedding 模型，把分块按 embedBatchSize 分批
// 送出去，再按原顺序拼回一整份。
//
// 【为什么它不是"索引一份文档"的全部】索引一份文档是性质相反的两半，
// 合在一个方法里就必然有一半做错（issue #97）：
//   - 这一半是对上游 HTTP 服务的调用，单份文档的处理上限是 30 分钟
//     （见 knowledge/river.go 的 Timeout）。调用方如果把它包进 InTx，这段
//     网络往返的每一秒都占着连接池的一个连接——worker 的 MaxWorkers 是 10，
//     连接池没配 pool_max_conns（pgx 默认 max(4, NumCPU)），10 个并发文档
//     任务就够把池攥干，同一队列里的文件清理、周期维护任务跟着一起堵住。
//     项目把这条规则写死在 internal/llm/usecase.go 和 internal/platform/db.go
//     里，Bootstrap 遵守了它（探测在事务开始之前），文档索引以前没有。
//   - 另一半（ReplaceChunks）只有放进事务里才能保证"删了旧分块但没插上
//     新的"不留库。
//
// 拆开之后调用方两头都要：这一半在事务外，那一半和状态置 ready 同事务
// （见 knowledge/usecase.go 的 ProcessDocument）。所以这里除了 ListModels
// 那次读之外不发任何 SQL，"事务里没有网络调用"这条性质因此是可断言的。
//
// 返回的 model 是落库时要写进 embedding_model 的人类可读模型名，不是内部
// 那行配置的 uuid——检索过滤用的是它。
func (u *Usecase) EmbedChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) ([][]float32, string, error) {
	// 【零分块直接返回，不解析模型也不发请求】空文档（空文件、只有空白的
	// 文件）不需要向量，也不该因为"还没有配置 embedding 模型"而失败——
	// 它根本没有要 embed 的东西。调用方那边仍然会清掉旧分块。
	if len(chunks) == 0 {
		return nil, "", nil
	}

	model, err := u.activeEmbeddingModel(ctx, q)
	if err != nil {
		return nil, "", fmt.Errorf("resolve active embedding model: %w", err)
	}

	embedder, err := u.registry.Embedder(ctx, model.ID.String())
	if err != nil {
		return nil, "", fmt.Errorf("get embedder for model %s: %w", model.ID, err)
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
			return nil, "", fmt.Errorf("embed chunks %d-%d of %d in document %s: %w",
				start+1, end, len(chunks), docID, err)
		}
		// 上游返回的向量数量和送进去的文本数量不一致——这不该发生，
		// 但发生的时候必须显式报错，不能假装对齐、悄悄错配某一段的向量。
		if len(batchVecs) != end-start {
			return nil, "", fmt.Errorf("embedder returned %d vectors for %d chunks: %w",
				len(batchVecs), end-start, platform.ErrUpstream)
		}
		vecs = append(vecs, batchVecs...)
	}

	return vecs, model.ModelID, nil
}

// ReplaceChunks 实现 knowledge.ChunkIndexer 的另一半：在同一份 q 上先删
// 后插，把文档上一次的分块换成本次的。
//
// 【为什么这一半必须能在事务里跑】调用方（knowledge.ProcessDocument）把
// 它连同一个"状态置 processing→ready"包进同一个事务（issue #97）。不然
// DELETE 与 INSERT 之间失败——连接断了、进程被杀、上游撤单——库里就只剩
// "旧分块删了、新的没插上"，而文档还停在 processing：检索一条都命中不到
// 它，直到下一次成功索引才恢复。事务把这段窗口变成"要么整体提交、要么
// 整体回滚"。所以这里的 q 允许是事务，repo 自己不 Committer 任何东西
// （见 knowledge/port.go 里 ChunkIndexer 的注释）。
//
// 【零分块也要删】一份内容变成空的文档（被清空的文件、只剩空白字符）
// 必须把上一版的分块清掉，否则检索还会命中已经不存在的文本。这时只删
// 不插，同样落在调用方的事务里。
func (u *Usecase) ReplaceChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk, vecs [][]float32, model string) error {
	if err := u.repo.DeleteByDocument(ctx, q, docID); err != nil {
		return fmt.Errorf("clear existing chunks of document %s before reindexing: %w", docID, err)
	}
	if len(chunks) == 0 {
		return nil
	}

	if err := u.repo.InsertChunks(ctx, q, docID, chunks, vecs, model); err != nil {
		return fmt.Errorf("insert %d chunks of document %s: %w", len(chunks), docID, err)
	}
	return nil
}

// DeleteByDocument 实现 knowledge.ChunkIndexer 的最后一半：删掉一个文档的
// 全部分块。它不碰网络，所以和 ReplaceChunks 一样可以传事务。
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
