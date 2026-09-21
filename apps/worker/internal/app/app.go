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
	"os"
	"os/signal"
	"syscall"
	"time"

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

// worker 关停的三个时间预算。三个数各自都有理由，不要合并成一个。
const (
	// workerSoftStopTimeout 是 River 给「正在跑的任务」留的收尾时间。
	//
	// 【不设它会发生什么】River 的 SoftStopTimeout 为 0 时，job 的 ctx
	// 继承 Start 的 ctx，而唯一能在 producer 停完之前取消 job ctx 的就是
	// 这个定时器。于是 Ctrl-C 会一直等到那份文档跑完——单份文档的上限是
	// 30 分钟（internal/knowledge/river.go 的 documentProcessingTimeout），
	// 那不叫优雅退出，叫挂死。
	//
	// 【为什么是 60 秒而不是 30 分钟】等 30 分钟没有意义：Windows 关控制台
	// 窗口只给几秒（Go 运行时把 CTRL_CLOSE_EVENT 映射成 SIGTERM 之后靠
	// block() 拖延，系统到点就 TerminateProcess），docker stop 默认 10 秒
	// 就 SIGKILL。60 秒覆盖的是「差几秒就跑完」这种常见情形；被截断的任务
	// 走正常失败路径交回 River 重试，不会丢。
	workerSoftStopTimeout = 60 * time.Second

	// workerStopTimeout 是 Stop 的总预算，比 SoftStopTimeout 多 15 秒：
	// 前者是「等任务收尾」，后者还要算上 producer 停轮询、队列维护服务
	// 退出这些收尾开销。
	workerStopTimeout = 75 * time.Second

	// workerCancelTimeout 是 Stop 超时之后 StopAndCancel 的预算。
	// 走到这一步说明有任务不听话，只能强取消它的 ctx。
	workerCancelTimeout = 15 * time.Second

	// workerFinalWait 是最后一次等 Stopped() 的上限。
	// 【必须有上限】`<-riverClient.Stopped()` 本身是无界的，两次 Stop 都
	// 失败时会永远挂在这里——那正好是用户最需要它能退出的时候。
	workerFinalWait = 5 * time.Second
)

// Run 装配并启动 worker 进程，阻塞到收到关停信号或出错。
func Run() error {
	// 顶层 ctx 挂在信号上，理由同 apps/api/internal/app/app.go：
	// Ctrl-C 与 SIGTERM 都要能触发关停。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

		// 关停时给正在跑的任务留的收尾时间，见文件头部的常量说明。
		SoftStopTimeout: workerSoftStopTimeout,

		// 【为什么改 rescue 的默认值】River 默认认为「跑了 1 小时还没结束的
		// 任务已经死了」（client.go 的 JobRescuerRescueAfterDefault），然后
		// 把它重新入队。默认值的前提是普通任务秒级完成；本项目单份文档的
		// 上限是 30 分钟，一个正常跑着的大文档会在 1 小时线附近被误判。
		//
		// 40 分钟 > 单任务上限 30 分钟（internal/knowledge/river.go），
		// 所以活着的任务不会被误判为 stuck；同时比默认的 1 小时更快地回收
		// 「worker 被 taskkill /F 杀掉」时留在 processing 的行——否则用户
		// 顶着界面上那个转圈的文档要等满一小时。
		//
		// 【代价要如实记住】River 文档明确警告：rescue 一个其实还活着的任务
		// 会导致同一份文档被处理两次。本机只有一个 worker，且 30 分钟硬上限
		// 保证活着的任务跑不到 40 分钟，所以这个风险可接受。
		RescueStuckJobsAfter: 40 * time.Minute,
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

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, stopping river client")
	case <-riverClient.Stopped():
		// 客户端在 Start 之后自己停了（内部致命错误之类）。
		// 这里直接收尾，不要再调 Stop。
		return nil
	}

	// 【先 stop() 再关停】ctx 已经取消之后 NotifyContext 仍占着信号处理器，
	// 不 stop() 的话第二次 Ctrl-C 会被吞掉，用户就没法强行退出了。
	stop()

	// 【为什么是 Stop 而不是直接 StopAndCancel】Stop 先停 producer（不再
	// 拉新任务），给在途任务最多 workerSoftStopTimeout 收尾，然后才取消
	// job ctx。文档处理是可恢复的，让它跑完比打断它更好。
	//
	// 【为什么超时之后还要 StopAndCancel】Stop 的 ctx 只管它自己等多久，
	// 不会取消已经在跑的任务。不补这一刀的话，一个卡住的任务会让 Stop
	// 返回错误、但 job 仍在跑，进程退不掉。
	//
	// 【为什么最后还要给 Stopped() 加个上限】正常情况下 Stop 返回时客户端
	// 已经停了，Stopped() 立刻返回。两次 Stop 都失败时才需要这个上限——
	// 那时如果无界地等下去，关停就变成了「按 Ctrl-C 挂死」。
	stopCtx, cancelStop := context.WithTimeout(context.Background(), workerStopTimeout)
	defer cancelStop()
	if err := riverClient.Stop(stopCtx); err != nil {
		logger.Warn("graceful river stop incomplete; cancelling remaining jobs", "error", err)

		cancelCtx, cancelCancel := context.WithTimeout(context.Background(), workerCancelTimeout)
		defer cancelCancel()
		if err := riverClient.StopAndCancel(cancelCtx); err != nil {
			logger.Error("river client did not stop", "error", err)
		}
	}

	select {
	case <-riverClient.Stopped():
		logger.Info("worker stopped cleanly")
	case <-time.After(workerFinalWait):
		logger.Error("river client did not report stopped; exiting anyway")
	}
	return nil
}
