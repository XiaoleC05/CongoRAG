package knowledge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

// statusWriteTimeout 是"用脱离 job 的 ctx 写终态"这一步的超时上限——见
// markFailed。
const statusWriteTimeout = 5 * time.Second

// allowedUploadExts 是 v1.0 能摄入的文件类型白名单。
//
// 【为什么服务端也要查一遍】前端的选择器写的是 accept=".md,.txt,.markdown"，
// 但那只是文件选择器的过滤条件，直接 curl -F 就能绕过。白名单不做的话，
// 一张 png 会被当纯文本切块、拿去 embedding、最后标成 ready——错误发生在
// 每一步都不报错的路径上，只有检索时的替换字符能看出来。
var allowedUploadExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
}

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
	// 扩展名按小写比对：.MD 和 .md 是同一类文件。
	if ext := strings.ToLower(path.Ext(filename)); !allowedUploadExts[ext] {
		return nil, fmt.Errorf(
			"unsupported file type %q: only .md, .markdown and .txt are accepted: %w",
			ext, platform.ErrInvalid)
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

	// storage_key 带上原始文件名的扩展名，是为了方便运维用肉眼辨认磁盘上
	// 的文件是什么类型。扩展名在这里不承担别的职责：能不能摄入在上面的
	// 白名单里已经判完了，落库用的 storage_key 不是判断依据。
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
// ListDocuments 取一个知识库下的文档，keyset 分页（issue #45）。
//
// 第二个返回值是下一页的游标，没有下一页时为空串。
func (u *Usecase) ListDocuments(ctx context.Context, kbID uuid.UUID, rawCursor string, limit int) ([]*Document, string, error) {
	limit, err := platform.ClampListLimit(limit)
	if err != nil {
		return nil, "", err
	}
	cur, err := platform.ParseCursor(rawCursor)
	if err != nil {
		return nil, "", err
	}

	docs, hasMore, err := u.docRepo.ListByKnowledgeBase(ctx, u.db, kbID, cur, limit)
	if err != nil {
		return nil, "", fmt.Errorf("list documents of knowledge base %s: %w", kbID, err)
	}

	// 下一页从最后一行接着走。列表非空时 hasMore 才可能为真，所以这里
	// 取末行是安全的。
	next := ""
	if len(docs) > 0 {
		last := docs[len(docs)-1]
		next = platform.EncodeNextCursor(hasMore, last.CreatedAt.Format(time.RFC3339Nano), last.ID.String())
	}
	return docs, next, nil
}

// ────────────────────────────────────────────────────────────────
// 重新索引（issue #39）
// ────────────────────────────────────────────────────────────────
//
// 【两条入口的差别只在粒度】单个文档是「这一份的向量坏了/是旧模型的，
// 重跑它」；整个知识库是「这个库里的都要重排」。换 embedding 模型的场景
// 更宽（影响全库），走的是 llm.Bootstrap 那条路，它通过下面的
// RequeueAllDocuments 复用同一段逻辑。
//
// 【为什么状态标记与入队必须在同一个事务里】只标记不入队会留下一批
// queued 但没人处理的文档；只入队不标记则任务跑起来时文档还是 ready，
// ProcessDocument 会落进 default 分支报"状态不对"。两者同生共死，
// 这正是 Upload 里已经在用的模式。

// ReindexDocument 把一份文档标回 queued 并重新排队处理。
//
// 文档不在可重建状态里（已经在排队、或 id 不存在）时返回
// platform.ErrConflict，由 handler 出 409。
func (u *Usecase) ReindexDocument(ctx context.Context, docID uuid.UUID) error {
	err := u.txm.InTx(ctx, func(q platform.Querier) error {
		if err := u.docRepo.MarkForReindex(ctx, q, docID); err != nil {
			return err
		}
		return u.enq.EnqueueProcessing(ctx, q, docID)
	})
	if err != nil {
		return fmt.Errorf("reindex document %s: %w", docID, err)
	}
	return nil
}

// ReindexKnowledgeBase 把一个知识库下所有可重建的文档重新排队，
// 返回这次真的排进去的数量。
func (u *Usecase) ReindexKnowledgeBase(ctx context.Context, kbID uuid.UUID) (int, error) {
	// 【先确认知识库存在】不查的话，"这个库里一份文档都没有"和"id 写错了"
	// 都返回 0，用户会以为重建成功了，实际上什么都没发生。
	if _, err := u.kbRepo.ByID(ctx, u.db, kbID); err != nil {
		return 0, fmt.Errorf("get knowledge base %s: %w", kbID, err)
	}

	enqueued := 0
	err := u.txm.InTx(ctx, func(q platform.Querier) error {
		ids, err := u.docRepo.MarkKnowledgeBaseForReindex(ctx, q, kbID)
		if err != nil {
			return err
		}
		if err := u.enq.EnqueueProcessingBatch(ctx, q, ids); err != nil {
			return err
		}
		enqueued = len(ids)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reindex knowledge base %s: %w", kbID, err)
	}
	return enqueued, nil
}

// RequeueAllDocuments 把所有可重建的文档重新排队，实现 llm.DocumentReindexer。
//
// 【它不自己开事务，q 由调用方给】换 embedding 模型的三步——清空旧向量、
// ALTER 列类型、全部文档重新入队——必须在**同一个事务**里完成。第三步如果
// 单独提交，「ALTER 成功但入队失败」会留下一个既没有旧向量、也没有任何任务
// 在重建的库，正是 #39 要消掉的那个状态。所以事务边界必须由调用方持有。
func (u *Usecase) RequeueAllDocuments(ctx context.Context, q platform.Querier) (int, error) {
	ids, err := u.docRepo.MarkAllForReindex(ctx, q)
	if err != nil {
		return 0, err
	}
	if err := u.enq.EnqueueProcessingBatch(ctx, q, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
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
		cutoff := time.Now().Add(-orphanGracePeriod)

		// tmp/ 里的半成品一起扫。WriteTemp 写失败时会自己删，但删除本身
		// 也可能失败（Windows 上句柄没关就是这种情况），那种文件对
		// List（只扫 rootDir 顶层）是不可见的，没有这一步就只增不减。
		// 用同一个宽限期：太新的临时文件可能正属于一次还在进行中的上传。
		if err := u.files.SweepTemp(ctx, cutoff); err != nil {
			return fmt.Errorf("sweep stale temp files: %w", err)
		}

		files, err := u.files.List(ctx)
		if err != nil {
			return fmt.Errorf("list files for orphan reconciliation: %w", err)
		}
		if len(files) == 0 {
			return nil
		}

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
// 【失败时写不写终态，取决于这是不是最后一次 attempt】isLastAttempt 由
// worker 按 River 的 job.Attempt/job.MaxAttempts 算好传进来：
//   - 不是最后一次：只把错误返回给 River，不动 documents.status。文档留在
//     processing，下一次投递读到"已经跑到这一步"接着往下跑——重试这才真的
//     能重跑。先写 failed 再交给 River 重试的话，下一次进来读到 failed 会
//     落进下面的 default 分支报 ErrConflict，重试机制就成了死代码。
//   - 是最后一次：River 不会再投递，必须落终态，否则文档会永久停在
//     processing（UI 会一直轮询它，且没有任何任务会把它推走）。
//
// 只有当状态是 ready 或 failed 时才拒绝——那两种状态出现在这里说明
// 有非预期的重复投递或状态被别处并发改动过，值得让它报错浮出来，
// 而不是悄悄再处理一遍已经完成或已经放弃的文档。
//
// 【failed 现在确实等于"已放弃"】终态由最后一次 attempt 落下，之后 River
// 不再投递。document.go 的 transitions 里 failed->queued（重试）与
// ready->queued（重新索引）这两条边现在**有调用点了**（issue #39 的
// ReindexDocument / ReindexKnowledgeBase / RequeueAllDocuments 走的是它们），
// 而下面的 switch 额外接受 StatusReady 是给"绕过入队路径直接插任务"和
// "River 重投"留的安全网。
func (u *Usecase) ProcessDocument(ctx context.Context, docID uuid.UUID, isLastAttempt bool) error {
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
	case StatusReady:
		// 【这是重新索引的安全网（issue #39）】正常路径下文档在入队时就已经
		// 被 CAS 回 queued 了，走不到这里。但 River 重投、或者有人绕过入队
		// 路径直接插任务时，文档可能还是 ready——不接住的话它会落进下面的
		// default，报一个"状态不对"的冲突，而正确答案就是重新处理它一遍。
		if err := u.docRepo.UpdateStatus(ctx, u.db, docID, StatusReady, StatusProcessing); err != nil {
			return fmt.Errorf("mark document %s processing (reindex): %w", docID, err)
		}
	case StatusFailed:
		// 【终态已达成，这次投递无事可做，返回 nil 而不是报错】
		// 确定性失败（比如内容不是合法 UTF-8）会直接把文档推进终态 failed，
		// 而那条分支返回的错误仍然会让 River 按 MaxAttempts 再投 24 次。
		// 那些投递如果落进下面的 default 分支报 ErrConflict，结果是 24 条
		// "状态冲突"日志，全都指向一个与真实原因（编码）无关的结论——
		// 排查时先看到的就是这些噪音。文档已经处理完了（结论是"失败"），
		// 这里当作成功收尾，让 River 不再重投。
		return nil
	default:
		return fmt.Errorf("document %s has unexpected status %s for processing: %w",
			docID, d.Status, platform.ErrConflict)
	}

	rc, err := u.files.Open(ctx, d.StorageKey)
	if err != nil {
		return u.failProcessing(ctx, docID, isLastAttempt,
			fmt.Errorf("open file %s of document %s: %w", d.StorageKey, docID, err))
	}
	content, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return u.failProcessing(ctx, docID, isLastAttempt,
			fmt.Errorf("read file %s of document %s: %w", d.StorageKey, docID, err))
	}

	// 【非 UTF-8 的内容必须在解析前拦住】parseAndChunk 最终会走 []rune，
	// 非法字节被悄悄换成 U+FFFD：产出一批"合法但毫无意义"的替换字符块，
	// 拿去 embedding 再入库，文档还标成 ready。同一份文件如果每段都短，
	// 原始非法字节会直达 chunk 的 INSERT，被 PostgreSQL 以 SQLSTATE 22021
	// 拒绝——同一种上传两种结局，只取决于段落长度。
	//
	// 【这一条不等最后一次 attempt】编码不会因为重试而改变，重试 25 次也是
	// 同一个结果，所以这里直接落终态，不交给 River 的重试。返回的错误仍然会
	// 让 River 重投，但那几次投递读回的是终态 failed，走上面那个 case 直接
	// 返回 nil——不会重跑、也不会刷出 24 条与真实原因无关的"状态冲突"日志。
	if !utf8.Valid(content) {
		u.markFailed(ctx, docID)
		return fmt.Errorf("document %s is not valid UTF-8 text: %w", docID, platform.ErrInvalid)
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
		return u.failProcessing(ctx, docID, isLastAttempt, err)
	}
	return nil
}

// failProcessing 统一处理"这一次 attempt 失败了"的收尾：只有最后一次
// attempt 才把文档推进终态 failed，其余情况原样把错误交回给 River——
// 状态留在 processing，下一次投递会接着跑（见 ProcessDocument 的注释）。
func (u *Usecase) failProcessing(ctx context.Context, docID uuid.UUID, isLastAttempt bool, cause error) error {
	if isLastAttempt {
		u.markFailed(ctx, docID)
	}
	return cause
}

// markFailed 把文档从 processing 推进终态 failed。
//
// 【ctx 必须活得过这个 job】调用它的时刻往往正是 job 已经失败的同一刻：
// job 超时时 River 会取消这个 ctx，而 pgx 拿着一个已取消的 ctx 连
// pgxpool.Acquire 那一步都过不去，UPDATE 一条都不会执行，文档就永远停在
// processing 里。收尾恰恰是最需要写成功的时候，所以这里摘掉取消信号
// （WithoutCancel）另起一个带超时的 ctx——脱开而不是彻底无界。
//
// 【错误不再丢掉】以前这里是 `_ =`，等于把"文档还卡在 processing"这件事
// 瞒了下来：没有日志、没有返回值、没有可观察的状态变化。现在至少在
// 写失败时留一条日志。
func (u *Usecase) markFailed(ctx context.Context, docID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusWriteTimeout)
	defer cancel()

	if err := u.docRepo.UpdateStatus(ctx, u.db, docID, StatusProcessing, StatusFailed); err != nil {
		slog.Default().Error("failed to mark document as failed",
			"document_id", docID, "error", err)
	}
}
