package knowledge

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// Usecase 是知识库和文档的业务层。
//
// 【本包只有一个 Usecase】知识库和文档是同一个包，就得是同一个 Usecase——
// 写成两个会是 `Usecase redeclared in this block`（代码架构设计 §5.3 已经
// 点名过这个坑）。
//
// 八个依赖对应八个职责：
//
//	kbRepo/docRepo   两张表各自的存取
//	files            上传写路径（写临时文件 → fsync → rename）
//	enq              把文档 ID 塞进 River 的处理队列
//	indexer          【唯一跨包 port】把切好的分块交给 retrieval 去 embed + 落库
//	cleaner          异步清理磁盘上不再需要的文件
//	sched            注册孤儿文件对账这个周期任务
//	txm              圈定"写文件记录 + 入队"这类需要原子性的操作边界
//	db               不需要事务的只读查询（List/Get 之类）直接用它
type Usecase struct {
	kbRepo  KBRepo
	docRepo DocRepo
	files   FileStore
	enq     Enqueuer
	indexer ChunkIndexer
	cleaner FileCleaner
	sched   platform.PeriodicScheduler
	txm     platform.TxManager
	db      platform.Querier
}

func NewUsecase(
	kbRepo KBRepo,
	docRepo DocRepo,
	files FileStore,
	enq Enqueuer,
	indexer ChunkIndexer,
	cleaner FileCleaner,
	sched platform.PeriodicScheduler,
	txm platform.TxManager,
	db platform.Querier,
) *Usecase {
	return &Usecase{
		kbRepo:  kbRepo,
		docRepo: docRepo,
		files:   files,
		enq:     enq,
		indexer: indexer,
		cleaner: cleaner,
		sched:   sched,
		txm:     txm,
		db:      db,
	}
}

// maxNameLen 是知识库名字的长度上限，按字符数（rune）算而不是字节数——
// 中文一个字三字节，按字节算的话限制会随语言变化。
//
// 数据库列是 text（无长度限制），所以这个上限由应用层兜。不设的话
// 客户端可以存进一个几十万字符的名字，列表接口的响应体跟着膨胀。
//
// 【改这个值时要同步改 contracts/openapi.yaml 里的 maxLength】
const maxNameLen = 200

// cleanName 校验并规整知识库名字。
//
// Create 和 Rename 共用，保证两个入口的规则一致。
// 先 trim 再判空：用户输入一串空格时，不 trim 会通过校验，
// 库里就会多一个没有名字的知识库。
// 返回处理过的名字，避免把首尾空格原样入库。
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("knowledge base name must not be empty: %w", platform.ErrInvalid)
	}
	if n := utf8.RuneCountInString(name); n > maxNameLen {
		return "", fmt.Errorf(
			"knowledge base name is too long (%d characters, max %d): %w",
			n, maxNameLen, platform.ErrInvalid)
	}
	return name, nil
}

// Create 新建一个知识库，返回造好的那一条。
//
// id 和两个时间戳在应用层生成，不用数据库的 DEFAULT：
// id 在写库之前就需要——写日志、直接返回给前端。
// 迁移里的 DEFAULT 作为兜底，供绕过应用层直接插数据时使用。
func (u *Usecase) Create(ctx context.Context, name string) (*KB, error) {
	name, err := cleanName(name)
	if err != nil {
		return nil, err
	}

	// 两个时间戳共用一个 now。创建这一刻它们应当完全相同；
	// 分两次调 time.Now() 只差几微秒，但会让数据看起来别扭。
	now := time.Now()

	kb := &KB{
		ID:        uuid.New(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// 传连接池而不是事务：新建只有一步写操作，不需要事务。
	if err := u.kbRepo.Insert(ctx, u.db, kb); err != nil {
		return nil, fmt.Errorf("create knowledge base: %w", err)
	}

	return kb, nil
}

// List 返回全部知识库。
//
// 目前没有过滤条件，所以直接转给 repo。
// 将来要加"隐藏已删除的""只显示当前用户的"这类规则时，加在这里，repo 不动。
func (u *Usecase) List(ctx context.Context) ([]*KB, error) {
	kbs, err := u.kbRepo.List(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	return kbs, nil
}

// Get 按 id 取一个知识库。
//
// 查不到时不做处理：repo 已经把 pgx.ErrNoRows 翻译成 platform.ErrNotFound，
// 原样往上传，由 handler 决定对应的 HTTP 状态码。
func (u *Usecase) Get(ctx context.Context, id uuid.UUID) (*KB, error) {
	kb, err := u.kbRepo.ByID(ctx, u.db, id)
	if err != nil {
		return nil, fmt.Errorf("get knowledge base %s: %w", id, err)
	}
	return kb, nil
}

// Rename 改知识库的名字。
func (u *Usecase) Rename(ctx context.Context, id uuid.UUID, newName string) error {
	newName, err := cleanName(newName)
	if err != nil {
		return err
	}

	// 不先 Get 确认存在，直接让 repo 去 UPDATE：
	// "先查再改"是两步操作，中间可能被别的请求删掉，查到了也不代表改的时候还在。
	// UPDATE 自己看 RowsAffected 判断更准，也少一次查询。
	//
	// updatedAt 由这里生成后传给 repo，不用 SQL 的 now()：
	// 否则 created_at（Go 生成）和 updated_at（数据库生成）来自两个时钟，
	// 开发期 api 在宿主机、PostgreSQL 在 Docker VM 里，两个时钟差几百毫秒，
	// 会出现 updatedAt 早于 createdAt 的异常数据。
	if err := u.kbRepo.Rename(ctx, u.db, id, newName, time.Now()); err != nil {
		return fmt.Errorf("rename knowledge base %s: %w", id, err)
	}
	return nil
}

// Delete 删除一个知识库，连带它下面的全部文档。
//
// 【为什么不再单纯依赖 ON DELETE CASCADE】文档上传做出来之前，这个方法
// 只有一句 DELETE，级联删除交给数据库。现在不够了：磁盘上的文件需要
// 异步清理，而清理需要知道"删掉的是哪些 storage_key"——级联删除只会
// 默默删掉数据库里的行，不会把这份名单交回来。所以改成显式调用
// docRepo.DeleteByKnowledgeBase（它用 DELETE ... RETURNING 拿到名单），
// ON DELETE CASCADE 仍然存在，只是现在它只负责 document_chunks 那一层——
// 这一层从来不需要磁盘清理（分块只存在数据库里）。
//
// 事务只包住两步数据库操作（删文档 + 删知识库），磁盘清理在事务外——
// 文件系统不支持回滚，把它塞进事务不会让它"跟着一起原子"，只会让它在
// 事务还没提交时就已经不可撤销地发生。
func (u *Usecase) Delete(ctx context.Context, id uuid.UUID) error {
	var storageKeys []string

	err := u.txm.InTx(ctx, func(q platform.Querier) error {
		keys, err := u.docRepo.DeleteByKnowledgeBase(ctx, q, id)
		if err != nil {
			return fmt.Errorf("purge documents of knowledge base %s: %w", id, err)
		}
		storageKeys = keys

		if err := u.kbRepo.Delete(ctx, q, id); err != nil {
			return fmt.Errorf("delete knowledge base %s: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 出了事务才清盘：宁可事务提交后清理失败留下孤儿文件（对账 job 会
	// 找到它们），也不要在事务还没提交、随时可能回滚时就已经删掉了
	// 一个数据库里还认领着的文件——方向原则见代码架构设计 §8.3：
	// 让清理更保守。
	u.cleaner.Schedule(storageKeys)
	return nil
}

// ────────────────────────────────────────────────────────────────
// 文档上传
// ────────────────────────────────────────────────────────────────

// maxUploadFilenameLen 是文件名的长度上限，和 maxNameLen 同样的理由
// （按字符数算，避免响应体随语言膨胀）；数字不必和知识库名字一致，
// 只是两处都需要一个上限，凑巧选了同一个量级。
const maxUploadFilenameLen = 255

// Upload 是写路径的完整实现，顺序锁死在这一个方法里，不能被拆开
// （代码架构设计 §5.4 / 开发文档 §4.7）：
//
//	① 写临时文件
//	② fsync
//	③ rename 为正式文件（用 UUID 当文件名，避免用户上传两个同名文件互相覆盖）
//	   ← 以上三步在事务之外，rename 不可回滚
//	④ 一个事务：INSERT document(status=queued) + 入队，同一个事务
//
// rename 必须先于事务：这样 status=queued 这一行只要存在，对应的文件
// 就必然已经落位——不会出现"数据库说排队中，但磁盘上根本没这个文件"。
// 如果 rename 成功之后事务失败，磁盘上会留下一个孤儿文件，那不是本方法
// 要处理的问题——它交给 StartReconciler 注册的周期任务事后清理。
func (u *Usecase) Upload(ctx context.Context, kbID uuid.UUID, filename string, r io.Reader) (*Document, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, fmt.Errorf("filename must not be empty: %w", platform.ErrInvalid)
	}
	if n := utf8.RuneCountInString(filename); n > maxUploadFilenameLen {
		return nil, fmt.Errorf(
			"filename is too long (%d characters, max %d): %w",
			n, maxUploadFilenameLen, platform.ErrInvalid)
	}

	// ① 写临时文件。byteSize 是服务端自己数出来的实际字节数，
	// 不是客户端上报的（那个不可信）。
	tmp, byteSize, err := u.files.WriteTemp(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	// Commit 成功后这是 no-op（临时文件已经被 rename 走了），
	// 用 defer 保证任何提前 return 的路径都不会漏删半成品临时文件。
	defer func() { _ = u.files.RemoveTemp(ctx, tmp) }()

	// storage_key 带上原始文件名的扩展名，纯粹是为了方便运维时用肉眼
	// 辨认磁盘上的文件是什么类型——扩展名不参与任何业务判断。
	storageKey := uuid.New().String() + path.Ext(filename)

	// ② fsync + ③ rename，必须在事务之前（见方法顶部的注释）。
	if err := u.files.Commit(ctx, tmp, storageKey); err != nil {
		return nil, fmt.Errorf("commit file: %w", err)
	}

	now := time.Now()
	d := &Document{
		ID:              uuid.New(),
		KnowledgeBaseID: kbID,
		Filename:        filename,
		StorageKey:      storageKey,
		Status:          StatusQueued,
		ByteSize:        byteSize,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// ④ 事务：INSERT + 入队。
	err = u.txm.InTx(ctx, func(q platform.Querier) error {
		if err := u.docRepo.Insert(ctx, q, d); err != nil {
			return fmt.Errorf("insert document %s: %w", d.ID, err)
		}
		if err := u.enq.EnqueueProcessing(ctx, q, d.ID); err != nil {
			return fmt.Errorf("enqueue processing for document %s: %w", d.ID, err)
		}
		return nil
	})
	if err != nil {
		// 事务失败：文件已经落位（rename 已成功），但数据库没有对应记录。
		// 这正是孤儿文件——不在这里补救（补救需要重试整个事务，可能是
		// 暂时性故障，也可能不是），交给对账 job 按 grace period 判断。
		return nil, fmt.Errorf("upload document: %w", err)
	}

	return d, nil
}

// ListDocuments 返回一个知识库下的全部文档。
func (u *Usecase) ListDocuments(ctx context.Context, kbID uuid.UUID) ([]*Document, error) {
	docs, err := u.docRepo.ListByKnowledgeBase(ctx, u.db, kbID)
	if err != nil {
		return nil, fmt.Errorf("list documents of knowledge base %s: %w", kbID, err)
	}
	return docs, nil
}

// GetDocument 按 id 取一份文档，主要给"查处理状态"这个端点用。
func (u *Usecase) GetDocument(ctx context.Context, id uuid.UUID) (*Document, error) {
	d, err := u.docRepo.ByID(ctx, u.db, id)
	if err != nil {
		return nil, fmt.Errorf("get document %s: %w", id, err)
	}
	return d, nil
}

// DeleteDocument 删除单个文档。
//
// 不开事务：只有一句 DELETE，document_chunks 由 ON DELETE CASCADE
// 自动清掉；磁盘清理照旧在事务外、异步（理由和 Delete(KB) 一样：
// 文件系统不支持回滚）。
func (u *Usecase) DeleteDocument(ctx context.Context, id uuid.UUID) error {
	storageKey, err := u.docRepo.Delete(ctx, u.db, id)
	if err != nil {
		return fmt.Errorf("delete document %s: %w", id, err)
	}
	u.cleaner.Schedule([]string{storageKey})
	return nil
}

// orphanGracePeriod 是孤儿对账 job 的宽限期：只清理"不在数据库里、且
// mtime 早于这个时长之前"的文件（开发文档 §4.7 给的区间是 10~30 分钟）。
//
// 【这是防 TOCTOU 竞争的关键】不设宽限期的话，对账 job 会在一次上传的
// rename 完成、但那次的 INSERT 事务还没提交之间的空档扫到磁盘，把一个
// 马上要被数据库认领的文件当成孤儿删掉——那不是"清理垃圾"，是删掉了
// 一个正在被使用的文件。
const orphanGracePeriod = 15 * time.Minute

// orphanReconcileInterval 是对账 job 自己的运行间隔。
const orphanReconcileInterval = 10 * time.Minute

// StartReconciler 注册孤儿文件对账这个周期任务，worker 进程启动时调用一次。
//
// 【为什么不能只用 DocRepo 做这件事】数据库自己不知道磁盘上有什么文件，
// 磁盘自己也不知道数据库认领了谁——孤儿判定天生需要同时看两侧。
// 所以这里先问 FileStore.List 拿到磁盘上的全部文件，筛出"足够旧"的那些
// 候选，再用 docRepo.ExistingStorageKeys 一次性查出这批候选里哪些还有
// 数据库记录认领着，剩下的才是真正的孤儿。
func (u *Usecase) StartReconciler(ctx context.Context) {
	u.sched.RegisterPeriodic("orphan-files", orphanReconcileInterval, func(ctx context.Context) error {
		files, err := u.files.List(ctx)
		if err != nil {
			return fmt.Errorf("list files for orphan reconciliation: %w", err)
		}
		if len(files) == 0 {
			return nil
		}

		cutoff := time.Now().Add(-orphanGracePeriod)
		candidates := make([]string, 0, len(files))
		for _, f := range files {
			if f.ModTime.Before(cutoff) {
				candidates = append(candidates, f.StorageKey)
			}
		}
		if len(candidates) == 0 {
			return nil
		}

		known, err := u.docRepo.ExistingStorageKeys(ctx, u.db, candidates)
		if err != nil {
			return fmt.Errorf("check existing storage keys: %w", err)
		}

		for _, key := range candidates {
			if known[key] {
				continue
			}
			// 删不掉只是浪费一点磁盘空间，不是数据错误——下一轮还会
			// 再试一次，不需要因为一个文件删失败就让整轮对账失败。
			_ = u.files.Remove(ctx, key)
		}
		return nil
	})
}

// ────────────────────────────────────────────────────────────────
// 文档处理管道（worker 进程调用）
// ────────────────────────────────────────────────────────────────

// ProcessDocument 是 DocumentProcessingWorker.Work 的全部业务逻辑
// （river.go 里那个 Worker 只是一层转接：解出 job.Args.DocumentID，
// 调这个方法）。只在 apps/worker 的装配里会被真正调用到——apps/api
// 的知识库 CRUD 路径从不触发它。
//
// 【为什么整个方法对重试是安全的】River 的任务可能因为进程崩溃、
// 网络抖动被重新投递。这个方法处理重复调用的方式是"继续往下跑，
// 而不是拒绝"：
//   - 状态已经是 processing（说明上一次已经推进到这一步）→ 直接继续，
//     不重新触发 queued->processing 那次 CAS（它现在会失败，但那是
//     预期内的，不代表这次调用本身错了）
//   - IndexDocument 内部先删后插（见 retrieval.Usecase.IndexDocument），
//     所以重新跑一遍分块+落库不会留下重复数据
//
// 只有当状态是 ready 或 failed 时才拒绝——那两种状态出现在这里说明
// 有非预期的重复投递或状态被别处并发改动过，值得让它报错浮出来，
// 而不是悄悄再处理一遍已经完成或已经放弃的文档。
func (u *Usecase) ProcessDocument(ctx context.Context, docID uuid.UUID) error {
	d, err := u.docRepo.ByID(ctx, u.db, docID)
	if err != nil {
		return fmt.Errorf("load document %s: %w", docID, err)
	}

	switch d.Status {
	case StatusQueued:
		if err := u.docRepo.UpdateStatus(ctx, u.db, docID, StatusQueued, StatusProcessing); err != nil {
			return fmt.Errorf("mark document %s processing: %w", docID, err)
		}
	case StatusProcessing:
		// 同一个任务被重试，已经跑到这一步了，往下继续。
	default:
		return fmt.Errorf("document %s has unexpected status %s for processing: %w",
			docID, d.Status, platform.ErrConflict)
	}

	rc, err := u.files.Open(ctx, d.StorageKey)
	if err != nil {
		u.markFailed(ctx, docID)
		return fmt.Errorf("open file %s of document %s: %w", d.StorageKey, docID, err)
	}
	content, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		u.markFailed(ctx, docID)
		return fmt.Errorf("read file %s of document %s: %w", d.StorageKey, docID, err)
	}

	chunks := parseAndChunk(content)

	err = u.txm.InTx(ctx, func(q platform.Querier) error {
		if err := u.indexer.IndexDocument(ctx, q, docID, chunks); err != nil {
			return fmt.Errorf("index document %s: %w", docID, err)
		}
		if err := u.docRepo.UpdateStatus(ctx, q, docID, StatusProcessing, StatusReady); err != nil {
			return fmt.Errorf("mark document %s ready: %w", docID, err)
		}
		return nil
	})
	if err != nil {
		u.markFailed(ctx, docID)
		return err
	}
	return nil
}

// markFailed 是失败路径的收尾。它自己的错误故意不往上传——调用方
// （ProcessDocument）已经有一个更有价值的原始错误要返回，markFailed
// 失败大概率是因为文档已经不在 processing 状态了（比如两次并发重试
// 都走到了失败分支），那种情况下"标记失败"这件事本身已经不重要。
func (u *Usecase) markFailed(ctx context.Context, docID uuid.UUID) {
	_ = u.docRepo.UpdateStatus(ctx, u.db, docID, StatusProcessing, StatusFailed)
}
