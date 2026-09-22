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

	"github.com/XiaoleC05/CongoRAG/internal/agent"
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

	// checkpointPruneInterval 是 Agent checkpoint 回收周期（issue #67）的
	// 执行间隔。
	//
	// 【为什么是小时级而不是和摘要维护一样的分钟级】回收的判据是
	// "终态之后 24 小时"（internal/agent 的 checkpointRetention），
	// 也就是说一小时跑一次和一分钟跑一次筛出来的行几乎完全一样——
	// 更短只会让这条整表 UPDATE 白跑。它要防的是"表随时间单调膨胀"，
	// 而那个问题的时间尺度是天，不是分钟。
	checkpointPruneInterval = time.Hour
)

// maintenanceQueue 是本进程消费的维护队列名（issue #129）。
//
// 【为什么需要一条单独的队列】理由见下面 river.Config.Queues 的注释：
// 文档处理单任务上限 30 分钟，单队列下一次批量上传就能把所有名额占满
// 数小时，周期维护任务（摘要/偏好抽取、记忆向量补算、事件与 checkpoint
// 回收、孤儿文件对账）只能排在后面，而且界面上没有任何信号说明它们只是
// 被排到了后面。
//
// 【这个名字的定义处不在这里】它在 internal/platform（MaintenanceQueue）
// ——因为任务进哪条队列是**插入时**决定的，而周期任务的插入点是那边的
// RegisterPeriodic。这里只是一个别名，让"下面 Config.Queues 里注册的
// 队列"和"那边插入时写的队列"由编译器保证是同一个字符串。对不上时的
// 表现是任务被投进一条没有消费者的队列：不报错、也不执行，维护工作静默
// 停摆。
//
// 【为什么常量不反过来放在这里让 platform 引用】apps/worker/internal/app
// 属于 apps/worker 的 internal 子树，Go 只允许 apps/worker 自己 import 它，
// platform 引用它会直接编译不过（internal 可见性 + 包循环）。
const maintenanceQueue = platform.MaintenanceQueue

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

	// 【worker 刻意不预热 tiktoken 词表（issue #44）】
	//
	// 它从不 tokenize：文档处理是按**字符**切块的
	// （internal/knowledge/pipeline.go 的 maxChunkChars），摘要与偏好维护
	// 按消息条数判断门槛（internal/conversation/memory.go 的两个常量），
	// 而唯一会算 token 的 Send 路径只在 apps/api 进程里跑。
	//
	// 【什么时候要回来看这一行】worker 开始数 token 的那天——比如按 token
	// 切块，或者记账（#47）改成由 worker 写 token_usage。那时这里要补上和
	// apps/api 一样的 llm.WarmupTokenizers 调用，否则 worker 会在运行期
	// 撞上"词表还没下载"这个启动期问题。
	// usageRepo 是 token 用量记账（issue #47）。worker 侧也要记：文档索引的
	// embedding、摘要压缩、偏好抽取都在这个进程里——只在 api 侧接会让这些
	// 调用完全不计。
	registry := llm.NewRegistry(llmRepo, box, pool, cfg.TiktokenCacheDir, llm.NewPgUsageRepo())
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
		// ── 两条队列：长任务不能阻塞短周期任务（issue #129）──
		//
		// 【一条队列会发生什么】文档处理单任务上限 30 分钟
		// （internal/knowledge/river.go 的 documentProcessingTimeout），
		// 一次上传 10 份大文档就能把 default 的 10 个名额占满数小时；
		// 而周期维护任务走的是同一条队列，于是长期记忆维护、checkpoint
		// 回收、孤儿文件对账全部延后，界面上却看不出任何异样——看起来
		// 就像那些功能坏了。解法是给周期维护任务一条自己的队列：文档
		// 塞满 default 时，maintenance 上的任务仍能按自己的节奏跑。
		//
		// 【路由在哪】任务进哪条队列是插入时决定的，那一步在
		// internal/platform/scheduler.go 的 periodicTaskConstructor
		// 里（经由那个桥注册的周期任务全部插入 maintenance）。本文件
		// 负责的是另一端——把这条队列注册成可消费的，两边用的名字同一个
		// 常量（maintenanceQueue），缺任何一端都会静默失效：少了那边的
		// InsertOpts，任务全落在 default，这条队列空转；少了这里的
		// Queues 条目，任务投进一条没人消费的队列，不报错也不执行。
		//
		// 【为什么名额是额外给的（10 + 3）而不是从 10 里割】割出去等于
		// 永久降低文档吞吐，换来的只是"每几分钟一次、单次几十秒"的维护
		// 任务不被阻塞——代价和收益不成比例。多出来的 3 个名额只在维护
		// 任务真的到期时才被用上，它们本身也是低频的。
		//
		// 【为什么是 3 个】同时注册的周期任务有六个（孤儿文件对账每 10
		// 分钟、conversation-summary 每 5 分钟、conversation-preferences
		// 每 10 分钟、conversation-memory-embeddings、conversation-events-prune、
		// agent-checkpoint-prune 每小时），到期时刻偶尔重叠，3 个名额足够
		// 让它们各自有位置，又不至于让六个整表扫描同时压库和同一个
		// embedding/LLM 服务。
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 10},
			maintenanceQueue:   {MaxWorkers: 3},
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

	// checkpoint 回收（issue #67）。它和上面那个"孤儿文件对账"是同一类问题
	// 的两侧：那个回收磁盘上没人认领的文件，这个回收数据库里已经没人会读的
	// checkpoint 快照。
	//
	// 【为什么放在 worker】它是周期性的整理工作，不是请求路径的一部分；
	// api 进程不该为了清理去起一个定时器。worker 本来就是"没有 HTTP、
	// 只消费队列"的进程，这里多注册一个周期任务不需要任何新机制
	// （platform.PeriodicScheduler）。
	//
	// 【为什么是置空 state_snapshot 而不是删 run 行】见 port.go 里
	// ClearTerminalRunCheckpoints 的注释：run 的历史还有价值
	// （轨迹页要读、用量要统计），膨胀的只有快照那一列。
	agentUC := agent.NewUsecase(
		agent.NewPgRepo(),
		agent.NewPgCheckpointStore(),
		agent.NewToolRegistry(), // 回收任务不碰工具，注册表留空
		registry,
		agent.NewPgIdempotencyStore(),
		txm,
		logger,
		pool,
	)
	// RegisterPeriodic 要的是 func(ctx) error，而 PruneCheckpoints 返回回收
	// 行数（它自己有日志）。这层薄包装把签名对上，顺带让"回收失败"这件事
	// 走周期任务的正常失败路径（River 会记下这次 job 失败）。
	sched.RegisterPeriodic("agent-checkpoint-prune", checkpointPruneInterval,
		func(ctx context.Context) error {
			_, err := agentUC.PruneCheckpoints(ctx)
			return err
		})

	// run_events 与 tool_effect_log 的保留窗口剪枝（issue #98）。这两张表和
	// checkpoint 一样是"只增不减"的，但判据各不相同（一张按时间、一张按
	// run 是否已终态），所以它们连同全部解释都在 internal/agent/retention.go
	// 里，装配根只负责说"把它挂上"——与会话那一侧的
	// convUC.StartMemoryMaintenance 同一个形状。
	agentUC.StartRetention(ctx, sched)

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
