// river.go 是本包唯一 import github.com/riverqueue/river 的文件——
// Enqueuer/FileCleaner 两个 port 由消费方（knowledge.Usecase）声明，
// 这个文件是它们的 River 实现。port.go/usecase.go 里没有一处出现 river
// 的类型，符合代码架构设计 §3 的表：port.go 只放 interface，实现在别处。
package knowledge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// DocumentProcessingArgs 是文档处理任务的参数。
//
// 只放 DocumentID：worker 拿到这个 ID 之后自己去数据库查文档的其余信息
// （文件名、storage_key），不把这些信息复制进任务参数——数据库里的那份
// 才是唯一真相，任务参数里存一份副本只会在文档信息变化时产生不一致。
type DocumentProcessingArgs struct {
	DocumentID uuid.UUID `json:"documentId"`
}

func (DocumentProcessingArgs) Kind() string { return "document_processing" }

// DocumentProcessingWorker 是唯一处理 DocumentProcessingArgs 的 River
// Worker，只在 apps/worker 的装配里被注册——apps/api 从不消费任务，
// 只往队列里插（riverEnqueuer 那一半）。
type DocumentProcessingWorker struct {
	river.WorkerDefaults[DocumentProcessingArgs]
	uc *Usecase
}

func NewDocumentProcessingWorker(uc *Usecase) *DocumentProcessingWorker {
	return &DocumentProcessingWorker{uc: uc}
}

// Work 只负责一件事：把 River 的重试计数翻译成"这是不是最后一次机会"。
//
// 【为什么这个判断在 worker 里】job.Attempt / job.MaxAttempts 是 River 的
// 概念，业务层不该认识它们；而"失败要不要落终态"正是由它们决定的——
// 不是最后一次就不落，让下一次投递接着跑（ProcessDocument 的注释里有
// 完整的理由）。Attempt 从 1 开始计，和 River 自己判断"还会不会重试"
// 用的是同一个比较。
func (w *DocumentProcessingWorker) Work(ctx context.Context, job *river.Job[DocumentProcessingArgs]) error {
	return w.uc.ProcessDocument(ctx, job.Args.DocumentID, job.Attempt >= job.MaxAttempts)
}

// documentProcessingTimeout 是单个文档处理任务的时间上限。
//
// 【为什么必须显式覆写】不写这个方法的话 WorkerDefaults.Timeout 返回 0，
// River 用 JobTimeoutDefault——1 分钟。而这个任务的实际内容是：读整个文件、
// 切分、再按批调 embedding 接口（retrieval 那边按 256 个分块一批）。上传
// 上限是 32 MiB（platform.Config.MaxUploadBytes），一份接近上限的纯文本文档
// 轻松就是几百批 embedding 调用，60 秒必然不够。超时的表现是 job ctx 被取消、
// 这一次 attempt 失败；能恢复的部分由 ProcessDocument 负责标记，但"每次
// 都超时"这件事本身会让这类文档永远处理不完。
//
// 【为什么是 30 分钟】和 platform.periodicTaskTimeout 取同一个量级——那边是
// 全表扫描，这边是一份大文档的全量向量化，都是"正常情况几十秒、最坏几分钟"
// 的量级，30 分钟足够容纳慢的本地 embedding 服务，又不至于让一个真卡住的
// 任务长时间占住 worker 名额（apps/worker 的 MaxWorkers 是 10）。
const documentProcessingTimeout = 30 * time.Minute

// Timeout 覆写 WorkerDefaults 的 0 值。理由见 documentProcessingTimeout。
func (w *DocumentProcessingWorker) Timeout(*river.Job[DocumentProcessingArgs]) time.Duration {
	return documentProcessingTimeout
}

var _ Enqueuer = (*riverEnqueuer)(nil)

type riverEnqueuer struct {
	client *river.Client[pgx.Tx]
}

// NewRiverEnqueuer 在 client 已经造好的场景下直接用（apps/api 是这种情况：
// 它不注册任何 Worker，构造 river.Client 不需要先有别的东西，没有循环依赖）。
func NewRiverEnqueuer(client *river.Client[pgx.Tx]) *riverEnqueuer {
	return &riverEnqueuer{client: client}
}

// SetClient 供 apps/worker 的两阶段装配用：那边造 river.Client 之前
// 必须先注册好 DocumentProcessingWorker，而 DocumentProcessingWorker
// 需要一个持有 Enqueuer 的 knowledge.Usecase——Usecase 得先存在，
// Enqueuer 却要等 Client 造好才能真正工作。用 NewRiverEnqueuer(nil) 先
// 占位，Client 造好之后调 SetClient 补上，和 platform.riverScheduler
// 解决同样问题的手法一致（见 platform/scheduler.go 的注释）。
func (e *riverEnqueuer) SetClient(client *river.Client[pgx.Tx]) {
	e.client = client
}

// EnqueueProcessing 必须在事务内调用（q 参数实际上必须是 pgx.Tx）——
// Upload 方法把它和 docRepo.Insert 放进同一个 u.txm.InTx 调用里，
// 这样"文档记录写入"和"任务入队"是同一个数据库事务的两句话：
// 事务提交，两者同时发生；事务回滚，两者一起消失。技术方案 §四称之为
// "事务性入队"，这是它在代码里的落地。
//
// 【为什么这里做类型断言而不是让 EnqueueProcessing 直接收 pgx.Tx】
// port.go 的 Enqueuer 接口收 platform.Querier，不是 pgx.Tx——
// 如果签名直接暴露 pgx.Tx，knowledge.Usecase（消费方）的方法签名就会
// 被迫认识 pgx，违反"业务层不认识数据库驱动"这条规则。断言失败只会
// 发生在有人误用这个接口（比如传了裸连接池而不是事务）的时候，
// 那种误用本身就该在开发时被这条报错挡住，而不是让 River 静默收到一个
// 脱离事务的 INSERT。
func (e *riverEnqueuer) EnqueueProcessing(ctx context.Context, q platform.Querier, documentID uuid.UUID) error {
	tx, ok := q.(pgx.Tx)
	if !ok {
		return fmt.Errorf("river enqueuer requires a transaction, got %T", q)
	}
	_, err := e.client.InsertTx(ctx, tx, DocumentProcessingArgs{DocumentID: documentID}, nil)
	if err != nil {
		return fmt.Errorf("enqueue document processing job for %s: %w", documentID, err)
	}
	return nil
}

// EnqueueProcessingBatch 是 EnqueueProcessing 的批量版，供重新索引用—
// 换 embedding 模型时全库都要重排，逐个 InsertTx 在文档上千时是一千条
// INSERT。InsertManyTx 同样要求传入事务，事务性入队这条性质不变。
//
// 【空列表直接返回】批量重建时"一个可重建的文档都没有"是完全正常的情况
//（新库、或者所有文档都还没处理过），不该因此报错。
func (e *riverEnqueuer) EnqueueProcessingBatch(ctx context.Context, q platform.Querier, documentIDs []uuid.UUID) error {
	if len(documentIDs) == 0 {
		return nil
	}
	tx, ok := q.(pgx.Tx)
	if !ok {
		return fmt.Errorf("river enqueuer requires a transaction, got %T", q)
	}

	params := make([]river.InsertManyParams, 0, len(documentIDs))
	for _, id := range documentIDs {
		params = append(params, river.InsertManyParams{
			Args: DocumentProcessingArgs{DocumentID: id},
		})
	}
	if _, err := e.client.InsertManyTx(ctx, tx, params); err != nil {
		return fmt.Errorf("enqueue %d document processing jobs: %w", len(documentIDs), err)
	}
	return nil
}

// FileCleanupArgs 是异步磁盘清理任务的参数。
//
// 每个 storage_key 一个独立任务，不是"一批 key 一个任务"：
// 单个任务失败（比如文件已经被手动删过）不该连累同一批里其余文件的清理，
// River 的重试是按任务粒度的，粒度越细，一次失败的影响范围越小。
type FileCleanupArgs struct {
	StorageKey string `json:"storageKey"`
}

func (FileCleanupArgs) Kind() string { return "file_cleanup" }

// FileCleanupWorker 是唯一处理 FileCleanupArgs 的 River Worker，
// 只在 apps/worker 注册——和 DocumentProcessingWorker 同样的道理，
// apps/api 只插任务（asyncFileCleaner.Schedule），从不消费。
//
// 【这个类型曾经缺失过】Schedule 会真的把任务插进 river_job 表，
// 但如果没有对应的 Worker 注册，River 找不到谁该处理这个 kind，
// 任务会一直停在 retryable 状态——不报错、不崩溃，只是永远排队。
// 这正是"不报错，只是结果不对"那类坑：磁盘清理表面上"启动"了
// （Schedule 返回 nil），实际上从未真正发生。
type FileCleanupWorker struct {
	river.WorkerDefaults[FileCleanupArgs]
	files FileStore
}

func NewFileCleanupWorker(files FileStore) *FileCleanupWorker {
	return &FileCleanupWorker{files: files}
}

func (w *FileCleanupWorker) Work(ctx context.Context, job *river.Job[FileCleanupArgs]) error {
	if err := w.files.Remove(ctx, job.Args.StorageKey); err != nil {
		return fmt.Errorf("remove file %s: %w", job.Args.StorageKey, err)
	}
	return nil
}

var _ FileCleaner = (*asyncFileCleaner)(nil)

type asyncFileCleaner struct {
	client *river.Client[pgx.Tx]
}

func NewAsyncFileCleaner(client *river.Client[pgx.Tx]) *asyncFileCleaner {
	return &asyncFileCleaner{client: client}
}

// SetClient：见 riverEnqueuer.SetClient 的注释，同样的两阶段装配需要。
func (c *asyncFileCleaner) SetClient(client *river.Client[pgx.Tx]) {
	c.client = client
}

// Schedule 用 Insert（不是 InsertTx）——它总是在事务外被调用（见
// Usecase.Delete 的注释：磁盘清理必须在数据库事务提交之后才安全），
// 所以这里没有 tx 可传，也不需要。
//
// 插入失败只记下来，不向上抛错：Schedule 的调用方（Delete）此刻已经
// 完成了真正重要的那部分（数据库记录已删除），磁盘清理是"最好能做到"
// 而不是"必须成功"的收尾工作——真的没入队成功，这几个文件会变成孤儿，
// 下一轮孤儿对账 job 会把它们找出来删掉，不会永久泄漏磁盘空间。
func (c *asyncFileCleaner) Schedule(storageKeys []string) {
	ctx := context.Background()
	for _, key := range storageKeys {
		if _, err := c.client.Insert(ctx, FileCleanupArgs{StorageKey: key}, nil); err != nil {
			// 故意不 return、不 panic：见上面的注释，这是收尾工作，
			// 失败有孤儿对账兜底。但仍要留痕，不能真的悄无声息——
			// 用 slog.Default() 而不是从构造函数传 logger 进来：
			// FileCleaner 的依赖只有一个 river.Client，为了一条兜底日志
			// 去改变整条装配链传 logger 不成比例；这条日志本身也不需要
			// 携带 request_id 之类的请求级上下文（Schedule 在事务外、
			// 脱离了原始 HTTP 请求的生命周期）。
			slog.Default().Error("failed to enqueue file cleanup job",
				"storage_key", key, "error", err)
		}
	}
}
