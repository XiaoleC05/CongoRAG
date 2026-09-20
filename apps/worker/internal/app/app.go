// Package app 是 worker 进程的装配根。
//
// 【为什么和 apps/api 的装配根重复十几行】代码架构设计 §6 明确记录过这条
// 权衡：worker 不能 import apps/api/internal/*（编译器会拦——Go 的
// internal 只对"internal 的父目录"子树可见），也不需要 gin/HTTP/EventSink。
// 为了消掉两边都要接 pgxpool/platform/llm.Registry 这点重复去造一个
// 共享装配包，代价是把 8 个业务包变成 9 个、还让两个进程互相绑死——
// 比留着这点重复更贵。
package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/ctxmgr"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/retrieval"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run 装配并启动 worker 进程，阻塞到 ctx 被取消或出错。
func Run() error {
	ctx := context.Background()

	cfg, err := platform.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := platform.NewLogger(cfg)

	// ── 基础设施 ────────────────────────────────────────
	//
	// 【和 apps/api 的池不同的地方】这个池要接 AfterConnect，注册
	// pgvector 的 pgx 编解码器——worker 是唯一真正把 []float32 编码成
	// halfvec 写进数据库的进程（retrieval.PgRepo.InsertChunks）。
	// apps/api 目前不碰向量列的值（BootstrapEmbedding 的 ALTER 是纯 DDL，
	// 不涉及向量编码），所以它的池不需要这一步——等 M2 检索端点接进 api
	// 进程时，那边的池也要补上同样的 AfterConnect。
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create pgx pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	logger.Info("database connected")

	box, err := platform.NewSecretBox(cfg)
	if err != nil {
		return fmt.Errorf("init secret box: %w", err)
	}

	// ── 叶子：各模块的 repo ─────────────────────────────
	kbRepo := knowledge.NewPgRepo()
	docRepo := knowledge.NewPgDocRepo()
	llmRepo := llm.NewPgConfigRepo()
	chunkRepo := retrieval.NewPgRepo()
	files := knowledge.NewLocalFileStore(cfg.DocumentsDir)

	registry := llm.NewRegistry(llmRepo, box, pool, cfg.TiktokenCacheDir)
	retrievalUC := retrieval.NewUsecase(chunkRepo, registry, llmRepo, pool)

	// ── 两阶段装配：Enqueuer / FileCleaner / Scheduler 先占位 ──
	//
	// 三者都需要 river.Client 才能真正工作，但 river.Client 的构造
	// 需要先有 Workers（里面要注册 DocumentProcessingWorker，它又需要
	// 一个持有 Enqueuer/Scheduler 的 knowledge.Usecase）——三者互相咬住，
	// 打破的办法是先造"空壳"，等 Client 真正造好后再 SetClient 补上。
	// 装配顺序本身就是这条依赖图的可执行校验：接不上、绑late 了，
	// 编译器和运行时至少一个会报错，不会是"看起来能跑但绑错了"。
	enqueuer := knowledge.NewRiverEnqueuer(nil)
	cleaner := knowledge.NewAsyncFileCleaner(nil)
	sched := platform.NewRiverScheduler()

	txm := platform.NewTxManager(pool)
	knowUC := knowledge.NewUsecase(kbRepo, docRepo, files, enqueuer, retrievalUC, cleaner, sched, txm, pool)

	// ── River：Workers 必须在 Client 构造之前注册好 ──
	workers := river.NewWorkers()
	river.AddWorker(workers, knowledge.NewDocumentProcessingWorker(knowUC))
	river.AddWorker(workers, knowledge.NewFileCleanupWorker(files))
	river.AddWorker(workers, platform.NewPeriodicTaskWorker(sched))

	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 10},
		},
		Workers: workers,
	})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}

	// 补上刚才占位的三个依赖。
	enqueuer.SetClient(riverClient)
	cleaner.SetClient(riverClient)
	sched.SetClient(riverClient)

	// 孤儿对账周期任务在这里注册——必须在 sched.SetClient 之后
	//（RegisterPeriodic 内部会用到 client，见 platform/scheduler.go）。
	knowUC.StartReconciler(ctx)

	// conversation.Usecase 在这个进程里主要是为了跑摘要维护/长期记忆
	// 抽取两个周期任务——LockedWriter/ChunkSearcher/ctxmgr.Manager 三个
	// 构造参数 MaintainSummary/ExtractPreferences 从不触碰(它们只服务
	// Send 这一条路径,worker 从不调 Send),但 NewUsecase 的签名要求
	// 一次性传全,和 apps/api 装配 retrievalUC "只为满足 ChunkIndexer
	// 依赖、自己从不调用"是同一种权衡的反面情况。
	convRepo := conversation.NewPgRepo()
	memRepo := conversation.NewPgMemoryRepo()
	ctxm := ctxmgr.NewUsecase(ctxmgr.NewLLMCompressor(registry))
	convUC := conversation.NewUsecase(
		convRepo,
		conversation.NewPgLockedWriter(pool),
		registry, llmRepo, ctxm, retrievalUC, memRepo,
		pool,
	)
	convUC.StartMemoryMaintenance(ctx, sched)

	logger.Info("starting river client")
	if err := riverClient.Start(ctx); err != nil {
		return fmt.Errorf("start river client: %w", err)
	}

	logger.Info("worker running")

	// 【暂无优雅退出】和 apps/api/internal/app/app.go 同样的现状：
	// ctx 是最朴素的 context.Background()，Ctrl-C 不会触发优雅退出。
	// M4-B 做优雅退出时，这里和 api 那边一起换成
	// signal.NotifyContext(...) + riverClient.Stop(ctx) + <-riverClient.Stopped()。
	//
	// 空 select 永久阻塞，让进程保持运行——它是 Go 规范里明确列出的
	// "终止语句"（terminating statement），函数在这里结束不需要再写
	// return，编译器不会因为"函数缺少返回值"报错。
	select {}
}
