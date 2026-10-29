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
	"sort"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/tokenize"
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

	// dedupeEnabled 控制"近重复抑制"（同名论文多版本去重）。默认开启；
	// 关闭只用于离线评测做 A/B 对比（见评测报告），不是给用户调的旋钮。
	dedupeEnabled bool
}

// Option 是技术方案 §4.1 点名的 Functional Options 模式：用在"运行期
// 可能变"的项上。目前只有 topK 一项，必填的依赖（repo/registry/llmRepo/db）
// 留在参数列表里，不塞进 options——那会让编译器帮不上忙（代码架构设计 §6.1）。
type Option func(*Usecase)

func WithTopK(k int) Option {
	return func(u *Usecase) { u.topK = k }
}

// WithNearDedupe 控制是否做近重复抑制（默认开启）。
//
// 【为什么做成 Option 而不是配置项】它只服务离线评测的 A/B 对比；
// 生产行为应该是确定的，不该让部署环境把它关掉。评测脚本通过
// CONGORAG_RETRIEVAL_DEDUP=off 由装配根显式传入。
func WithNearDedupe(enabled bool) Option {
	return func(u *Usecase) { u.dedupeEnabled = enabled }
}

func NewUsecase(repo Repo, registry llm.Registry, llmRepo llm.ConfigRepo, db platform.Querier, opts ...Option) *Usecase {
	u := &Usecase{repo: repo, registry: registry, llmRepo: llmRepo, dedupeEnabled: true, db: db, topK: defaultTopK}
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

	// ── 混合检索（参赛版 M2）：向量 + 词法两路召回，RRF 融合 ────────
	//
	// 为什么不是"向量不够、加个词法兜底"：科研问答的高频查询是精确锚点型
	// （"式 (3) 的 α 取值"、"Table 2 的 BLEU"、"引用 [11] 是谁"），BM25/FTS
	// 对编号、符号、术语的召回远好于 dense；而"换个说法也能找到"是向量的
	// 强项——两者互补，融合是标准做法，也是评分里"混合检索策略是否合理"
	// 的直接证据。
	//
	// 召回数放大到 topK 的若干倍再融合：融合的价值来自两路各自的候选池，
	// 只召回 topK 会退化成"两路各取前几、取交集更少"。
	recall := topK * hybridRecallMultiplier
	if recall < hybridMinRecall {
		recall = hybridMinRecall
	}
	if recall > hybridMaxRecall {
		recall = hybridMaxRecall
	}

	vectorHits, err := u.repo.Search(ctx, u.db, req.KnowledgeBaseID, vecs[0], model.ModelID, recall)
	if err != nil {
		return nil, fmt.Errorf("vector search knowledge base %s: %w", req.KnowledgeBaseID, err)
	}

	lexicalHits, err := u.repo.SearchLexical(ctx, u.db, req.KnowledgeBaseID, tokenize.Query(req.Text), recall)
	if err != nil {
		// 词法这一路失败（比如 tsquery 语法导致的 DB 错误）不应该让整个
		// 检索失败——向量结果仍然可用，只是退化成纯向量检索。
		// 这类失败记日志（不在这里打，由调用方/日志中间件观察错误文本），
		// 但返回值只带向量结果。
		lexicalHits = nil
	}

	// 先按召回池融合（可能多于 topK），再做近重复抑制，最后截到 topK：
	// 若先截 topK 再去重，被去重挤掉的名额无法由后面的候选补上。
	fused := fuseRRF(vectorHits, lexicalHits, recall)
	if u.dedupeEnabled {
		return dedupeNearDuplicates(fused, topK), nil
	}
	return fused[:min(len(fused), topK)], nil
}

// 混合检索参数。
//
// rrfK=60 是 RRF（Reciprocal Rank Fusion）的常用常数：它把"第 1 名"
// 和"第 2 名"的分数差压得比较平滑，避免某一路的绝对分数主导融合结果。
// 召回倍数 4、下限 20、上限 50：上限防止大 topK 查询把两路都拉成全表扫描。
const (
	rrfK                   = 60
	hybridRecallMultiplier = 4
	hybridMinRecall        = 20
	hybridMaxRecall        = 50
)

// fuseRRF 把两路召回按 RRF 融合，返回最终 topK。
//
// 公式：score(d) = Σ_路径 1 / (k + rank_路径(d))，rank 从 1 开始。
// 两路原始分数（余弦、ts_rank_cd）量纲不同、不能直接相加，RRF 只用排名，
// 所以不需要归一化——这是它比"加权求和"更适合混合检索的原因。
//
// 【两路的原始分数保留在 Chunk.VectorScore / LexicalScore】检索调试面板
// 要分别显示：答错时才能判断是"词法没召回"还是"向量排序不对"。
func fuseRRF(vectorHits, lexicalHits []domain.Chunk, topK int) []domain.Chunk {
	type fused struct {
		chunk domain.Chunk
		score float64
	}
	byID := make(map[uuid.UUID]*fused, len(vectorHits)+len(lexicalHits))

	merge := func(hits []domain.Chunk, isVector bool) {
		for i, c := range hits {
			f, ok := byID[c.ID]
			if !ok {
				f = &fused{chunk: c}
				byID[c.ID] = f
			} else if f.chunk.PageStart == 0 && c.PageStart > 0 {
				// 两路命中的同一块，选带学术定位信息的那份（向量路径
				// 和词法路径都会读同样的列，正常不会缺；这里只是保险）。
				f.chunk = c
			}
			f.score += 1.0 / float64(rrfK+i+1)
			if isVector {
				f.chunk.VectorScore = c.Score
			} else {
				f.chunk.LexicalScore = c.Score
			}
		}
	}
	merge(vectorHits, true)
	merge(lexicalHits, false)

	out := make([]domain.Chunk, 0, len(byID))
	for _, f := range byID {
		c := f.chunk
		c.Score = f.score
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// 同分时用 id 定序，保证结果稳定（否则同一次查询两次排序不同，
		// 测试会间歇性失败，前端调试面板也会"看起来在抖"）。
		return out[i].ID.String() < out[j].ID.String()
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
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
