package knowledge

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// KBRepo 是知识库的存取接口。
//
// 接口由消费方声明：它描述业务层需要什么能力，而不是 PostgreSQL 能做什么。
// 实现它的类型在 postgres.go，换实现（内存版、别的存储）时业务层不用改。
//
// 每个方法的 q 参数是 platform.Querier，连接池和事务都满足它：
//
//	传连接池 → SQL 立刻执行、立刻提交
//	传事务   → SQL 进入该事务，由调用方决定提交还是回滚
//
// 所以事务边界由调用方决定，repo 不自己开事务。
type KBRepo interface {
	// Insert 插入一个知识库。
	Insert(ctx context.Context, q platform.Querier, kb *KB) error

	// List 返回全部知识库，按创建时间倒序。
	List(ctx context.Context, q platform.Querier) ([]*KB, error)

	// ByID 按 id 取一个知识库，找不到时返回 platform.ErrNotFound。
	//
	// 翻译成 platform 的错误而不是新造一个：上层靠 errors.Is 判断，
	// 自己新造的错误它认不出来。
	ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*KB, error)

	// Rename 改知识库的名字，同时刷新 updated_at。
	//
	// updatedAt 由调用方传入，不用 SQL 的 now()：
	// created_at 由 Go 生成，updated_at 如果由数据库生成，
	// 同一个概念就有了两个时钟来源。
	// 开发期 api 跑在宿主机、PostgreSQL 跑在 Docker VM 里，
	// 两个时钟可能差几百毫秒，导致 updatedAt 早于 createdAt。
	// 交付期两者同在一个 VM 里不会出问题，但不应依赖这个巧合。
	Rename(ctx context.Context, q platform.Querier, id uuid.UUID, newName string, updatedAt time.Time) error

	// Delete 删除一个知识库。
	//
	// 只删 knowledge_bases 这一行，它下面的 documents 和 document_chunks
	// 由迁移里的 ON DELETE CASCADE 自动删除。
	Delete(ctx context.Context, q platform.Querier, id uuid.UUID) error
}

// ────────────────────────────────────────────────────────────────
// 文档
// ────────────────────────────────────────────────────────────────

// DocRepo 是文档的存取接口。
//
// 名字必须是 DocRepo，不能叫 Repo——同一个包里已经有 KBRepo，
// §5.3 的 Usecase 同时持有它们，两个接口各自一个名字才不会混。
type DocRepo interface {
	Insert(ctx context.Context, q platform.Querier, d *Document) error

	ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*Document, error)

	// UpdateStatus 做状态迁移，实现里会先用 Status.CanTransition 校验，
	// 再在 SQL 里加 WHERE status = $from 做 CAS，两层防护（业务规则 +
	// 并发保护）。不满足任何一层都返回 platform.ErrConflict。
	UpdateStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to Status) error

	// ListByKnowledgeBase 按 created_at 倒序取一个知识库下的文档，最多 limit 条。
	//
	// 【keyset 分页，不是 offset】游标是第一页最后那一行的 (created_at, id)。
	// 用 cursor 而不是 OFFSET 的两个理由：文档会被上传（新行插在头部），
	// OFFSET 分页在插入之后会让第一页的尾部在第二页重复出现；而且这个列表
	// 被前端每 2 秒轮询一次，OFFSET 每页都要让 Postgres 扫过并丢弃前 m 行。
	//
	// 【cur 为 nil 表示第一页】limit 由调用方（usecase）校验过区间。
	//
	// 返回值里的 bool 是 hasMore：还有没有下一页。它是**多取一条**得来的，
	// 不是 COUNT(*)——见实现里的注释。
	//
	// 【两个并列键缺一不可】同一毫秒创建的两行 created_at 相同，只按时间
	// 翻页会稳定地多出或漏掉一行。id 作为第二排序键让它成为严格全序。
	ListByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID, cur *platform.ListCursor, limit int) ([]*Document, bool, error)

	// Delete 删除单个文档，返回它的 storage_key（调用方用它异步清理磁盘）。
	// 找不到时返回 platform.ErrNotFound——和 DeleteByKnowledgeBase 不同，
	// 那个方法删的是"一批，可能是零个"，天然幂等；这里删的是"一个具体的东西"，
	// 客户端指名删一个不存在的文档，报错比默默当成功更诚实。
	Delete(ctx context.Context, q platform.Querier, id uuid.UUID) (storageKey string, err error)

	// DeleteByKnowledgeBase 删除一个知识库下的全部文档，返回被删文档的
	// storage_key 列表，调用方（Usecase.Delete）用它们去异步清理磁盘文件。
	//
	// 【故意不依赖 ON DELETE CASCADE】级联删除会删掉数据库里的行，但不会
	// 告诉调用方"删了哪些 storage_key"——而磁盘文件的清理恰恰需要这份名单。
	// 所以这里用显式 DELETE ... RETURNING，级联删除仍然存在（保护
	// document_chunks），只是不再是清理磁盘的唯一依据。
	DeleteByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]string, error)

	// ExistingStorageKeys 在给定的一批 storage_key 里，返回其中真的被某个
	// document 引用着的那些。孤儿对账用它判断"磁盘上这个文件还有主吗"。
	//
	// 【这是对方案里 OrphanCandidates 签名的修正】原始签名
	// `OrphanCandidates(ctx, q, before time.Time) ([]string, error)` 只靠
	// 一个时间参数、不接触文件系统，无法回答"磁盘上哪些文件没人认领"——
	// 这个问题的答案必须同时看"磁盘上有什么"（FileStore.List）和
	// "数据库认领了什么"，任何单独一侧都给不出来。真正的对账逻辑挪到
	// Usecase.StartReconciler 里做（见 usecase.go），这个方法只负责
	// DocRepo 那一半：批量查存在性。
	ExistingStorageKeys(ctx context.Context, q platform.Querier, keys []string) (map[string]bool, error)

	// ── 重新索引（issue #39）──────────────────────────────────
	//
	// 【为什么是批量 UPDATE 而不是逐行 UpdateStatus】三个方法都要改一批行，
	// 逐行做就是 N 条 SQL；而且 UpdateStatus 的 CAS 只认单一起点状态，
	// 这里要同时覆盖 ready / failed / processing 三种。
	//
	// 【这三个方法必须是无分页的全量语义】将来给列表查询加 limit/offset 时，
	// 绝不能把它们改成复用带分页的列表方法——重建一个 500 份文档的库却只
	// 排了 20 份，而且不报错，是这条路径上最像"看起来成功了"的缺陷。

	// MarkForReindex 把一份文档标回 queued，供单文档重建用。
	// 状态不在可重建集合里（比如正在 processing 或已经在 queued）时返回
	// platform.ErrConflict，和 UpdateStatus 同一个约定。
	MarkForReindex(ctx context.Context, q platform.Querier, id uuid.UUID) error

	// MarkKnowledgeBaseForReindex 把一个知识库下所有可重建的文档标回
	// queued，返回它们的 id（调用方用它批量入队）。
	MarkKnowledgeBaseForReindex(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]uuid.UUID, error)

	// MarkAllForReindex 把所有可重建的文档标回 queued，返回它们的 id。
	// 换 embedding 模型时用它——那件事影响的是全库，不只是某一个知识库。
	MarkAllForReindex(ctx context.Context, q platform.Querier) ([]uuid.UUID, error)
}

// FileStore 把"写临时文件 → fsync → rename"这条写路径封成接口，
// 让 knowledge.Usecase 不直接 import os。
type FileStore interface {
	// WriteTemp 把 r 的内容写进一个临时文件，返回它的路径和实际写入的字节数。
	//
	// 【字节数从这里来，不是从 multipart.FileHeader.Size 来】表单头里的
	// Size 是客户端上报的，不可信；这里的字节数是服务端自己数出来的，
	// 落进 documents.byte_size 的必须是这一个。
	WriteTemp(ctx context.Context, r io.Reader) (tmpPath string, size int64, err error)

	// Commit 把临时文件 fsync 后 rename 成正式文件（用 storageKey 当文件名）。
	// rename 在 POSIX 上是原子的：调用方看到的只有"文件在"或"文件不在"，
	// 不会看到"文件写了一半"。
	Commit(ctx context.Context, tmpPath, storageKey string) error

	Open(ctx context.Context, storageKey string) (io.ReadCloser, error)

	// RemoveTemp 删掉临时文件。Commit 成功之后临时文件已经被 rename 走了，
	// 此时调用它是 no-op（文件已经不在原路径上，不算错误）。
	RemoveTemp(ctx context.Context, tmpPath string) error

	Remove(ctx context.Context, storageKey string) error

	// List 列出所有正式文件（不含 tmp/ 目录），供孤儿对账使用。
	List(ctx context.Context) ([]FileInfo, error)

	// SweepTemp 删掉 tmp/ 目录里 mtime 早于 olderThan 的临时文件，返回
	// 删除过程中遇到的错误（单个文件删不掉只记日志，不让整轮清扫失败）。
	//
	// 【为什么需要一个专门的清扫口】WriteTemp 失败时会自己删掉半成品，
	// 但删除本身也可能失败（句柄还没关、被杀毒软件占着），那条路径返回的
	// 路径是空串，调用方拿不到文件名、也就没法补一刀。而 tmp/ 对
	// FileStore.List 是不可见的（它只扫 rootDir 顶层），孤儿对账因此看不见
	// 这些文件——没有这个口子的话它们只增不减，谁也观察不到。
	SweepTemp(ctx context.Context, olderThan time.Time) error
}

// FileInfo 是 List 返回的一条文件信息。
type FileInfo struct {
	StorageKey string
	ModTime    time.Time
}

// Enqueuer 是 River 的窄接口，让 knowledge 不需要 import river——
// 只声明"我需要能把一个文档 ID 塞进处理队列"这一个能力。
type Enqueuer interface {
	EnqueueProcessing(ctx context.Context, q platform.Querier, documentID uuid.UUID) error

	// EnqueueProcessingBatch 一次把多份文档排进队列，供重新索引用。
	//
	// 【为什么要批量】换 embedding 模型时要把全库的文档重新排队，逐个
	// InsertTx 在文档上千时是一次请求上千条 INSERT。批量版走 River 的
	// InsertManyTx，仍然是事务性的（与状态标记同事务）。
	EnqueueProcessingBatch(ctx context.Context, q platform.Querier, documentIDs []uuid.UUID) error
}

// ChunkIndexer 是【规则 A】的例子：knowledge 声明它需要什么，
// retrieval.Usecase 实现它——knowledge 因此不需要 import llm
// （embed 和落库都在 retrieval，见代码架构设计 §2.1 的警告）。
//
// 【为什么拆成 EmbedChunks / ReplaceChunks 两个方法（issue #97）】
// 它们对应"索引一份文档"里性质完全相反的两半：EmbedChunks 只发对上游
// embedding 服务的 HTTP 调用（单份文档的处理上限是 30 分钟，见 river.go
// 的 Timeout），ReplaceChunks 只在数据库里先删后插。
//
// 合成一个方法的话，调用方只有两种选择，而两种都错：
//   - 把整段网络往返包进 InTx：事务握着连接池的一个连接等 HTTP——worker
//     的 MaxWorkers 是 10，连接池没配 pool_max_conns（pgx 默认
//     max(4, NumCPU)），几个并发文档任务就能把池攥干，把同一队列里的文件
//     清理、周期维护任务一起堵死。项目把"事务里不要做慢速网络调用"写死在
//     internal/llm/usecase.go 和 internal/platform/db.go，Bootstrap 遵守了它。
//   - 完全不开事务：DELETE 与 INSERT 之间失败会留下"旧分块删了、新的没插上"
//     的中间态，而文档停在 processing——检索一条都命中不到它，直到下一次
//     成功索引才恢复。
//
// 拆开之后调用方两头都要：EmbedChunks 在事务外，ReplaceChunks 与"状态置
// ready"在同一个事务里（见 knowledge/usecase.go 的 ProcessDocument）。
//
// 因此两个方法的 q 语义不同，这也是 port 里唯一一处因方法而异的约定：
//   - EmbedChunks 的 q 必须是**连接池**，不能是事务（它要发网络请求）；
//   - ReplaceChunks 的 q 可以是事务——"删了旧分块但没插上新的"不留库，
//     正是靠调用方把事务传进来实现的；repo 自己不 Committer 任何东西。
//
// DeleteByDocument 不碰网络，跟着 ReplaceChunks 的约定走（可以传事务）。
type ChunkIndexer interface {
	// EmbedChunks 计算分块的向量，只发对上游的 HTTP 调用，不写任何 SQL
	// （读一次模型配置除外）。返回的 model 是落库时要写进 embedding_model
	// 的人类可读模型名，vecs 与 chunks 按下标一一对应。
	//
	// 零个分块时返回 (nil, "", nil)：空文档没有要 embed 的东西，也不该
	// 因为"还没配 embedding 模型"而失败——调用方仍然要清掉它的旧分块。
	EmbedChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) (vecs [][]float32, model string, err error)

	// ReplaceChunks 把文档上一次的分块换成本次的：先删后插。只有这一半
	// 允许在事务里跑。
	//
	// 【零分块也要删】一份内容变成空的文档（被清空的文件、只剩空白字符）
	// 必须把上一版的分块清掉，否则检索还会命中已经不存在的文本。
	//
	// 【先删后插是重试安全性的核心】River 的任务可能被重投，同一个文档会
	// 重新跑一遍：不先清空的话，表里会留下上一次已经插入的部分分块，重试
	// 一次就多一份重复数据。先删后插让这个方法本身对重试幂等。
	ReplaceChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk, vecs [][]float32, model string) error

	// DeleteByDocument 删掉一个文档的全部分块（删文档、或单独清索引时用）。
	DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error
}

// FileCleaner 是磁盘文件的异步清理。
//
// 【为什么是 Schedule 不是 Remove】删知识库时，磁盘清理必须在事务外
// （文件系统不支持回滚，见代码架构设计 §8.3），而且不该阻塞住那次
// HTTP 请求——用户点"删除"不该等磁盘 I/O。Schedule 把 storageKeys 交给
// 异步任务，方法本身立刻返回。
type FileCleaner interface {
	Schedule(storageKeys []string)
}
