// Package app 是 api 进程的装配根。
//
// 这里是全 api 唯一 new 具体类型的地方，其他所有文件只认 interface。
// 装配顺序即依赖图的拓扑序，接不上就说明某个包的依赖方向错了。
//
// worker 有它自己的装配根（apps/worker/internal/app），两边重复十几行是正常的，
// 不要为了消重造共享装配包。
package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/XiaoleC05/CongoRAG/apps/api/internal/api"
	"github.com/XiaoleC05/CongoRAG/internal/agent"
	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/ctxmgr"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/retrieval"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run 装配并启动 HTTP 服务，阻塞到服务退出或出错。
//
// webFS 是内嵌的前端产物，由 main.go 用 go:embed 传进来
// （embed 指令只能嵌"同目录及以下"，所以它必须写在 main.go 里）。
func Run(webFS embed.FS) error {
	// 顶层 ctx 挂在信号上：Ctrl-C（Windows 的 os.Interrupt）和 SIGTERM
	// （Linux 容器停机的标准信号）都会取消它，关停流程从这里开始。
	//
	// 【o.Interrupt 在 Windows 上够用】Windows 没有 SIGTERM，Go 运行时把
	// 控制台 CTRL_C_EVENT / CTRL_BREAK_EVENT 映射成 os.Interrupt，把
	// CTRL_CLOSE_EVENT / CTRL_SHUTDOWN_EVENT 映射成 syscall.SIGTERM。
	// 两个都注册上，开发期（Windows）和交付期（Linux 容器）就都能触发。
	//
	// 【stop 是延迟调用的，但关停路径里还会再显式调一次】原因见下面
	// 收到信号之后那段——不提前 stop() 的话第二次 Ctrl-C 会被吞掉。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := platform.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := platform.NewLogger(cfg)

	// ── 基础设施 ────────────────────────────────────────
	//
	// 【为什么这个池也要注册 pgvector 编解码器】M2 起，conversation.Usecase.Send
	// 会调用 retrieval.Usecase.Search，Search 内部把查询文本的 []float32
	// 向量编码成 halfvec 查询参数——这个池因此需要和 apps/worker 那个池
	// 同样的 AfterConnect 钩子。M1 阶段（只有 BootstrapEmbedding 的
	// ALTER，纯 DDL，不编码向量参数）曾经不需要这一步，现在需要了。
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

	// 启动时就验证数据库真的连得上，不要等到第一个请求才发现配错了
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	logger.Info("database connected")

	// SecretBox 三级来源见 internal/platform/secretbox.go 的注释。
	// 放在数据库连上之后、装配业务层之前——它自己不需要数据库，
	// 但 llm.Usecase 需要它，顺序上排在这里最自然。
	box, err := platform.NewSecretBox(cfg)
	if err != nil {
		return fmt.Errorf("init secret box: %w", err)
	}

	// River 的 api 侧客户端：只插任务，从不消费（Queues/Workers 都留空——
	// River 支持这种"insert-only"用法）。上传文档时 Upload 方法要用它
	// 把处理任务和 DB 记录放进同一个事务（riverClient.InsertTx），
	// 真正跑这些任务的是 apps/worker 那个独立进程。
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}

	// ── 叶子：各模块的 repo ─────────────────────────────
	// repo 是无状态的：连接由 usecase 每次调用时作为 q 传入。
	kbRepo := knowledge.NewPgRepo()
	docRepo := knowledge.NewPgDocRepo()
	llmRepo := llm.NewPgConfigRepo()
	chunkRepo := retrieval.NewPgRepo()
	files := knowledge.NewLocalFileStore(cfg.DocumentsDir)

	// ── 业务：各模块的 usecase ──────────────────────────
	// 顺序即依赖图拓扑序：repo → usecase。
	//
	// llm.Registry 是本项目唯一 import Eino 的入口（internal/llm/eino.go）。
	// 装配根这里只认识 llm.Registry 这个接口，不知道 Eino 存在。
	registry := llm.NewRegistry(llmRepo, box, pool, cfg.TiktokenCacheDir)
	llmUC := llm.NewUsecase(llmRepo, box, registry, platform.NewTxManager(pool), pool)

	// retrieval.Usecase 在这个进程里只是为了满足 knowledge.Usecase 的
	// ChunkIndexer 依赖——真正调用 IndexDocument 的是 apps/worker 的
	// ProcessDocument，api 进程从不触发它。两边各自装一份是
	// "worker 有自己的装配根"这条权衡的自然结果（见文件顶部注释）。
	retrievalUC := retrieval.NewUsecase(chunkRepo, registry, llmRepo, pool)

	// sched 在这个进程里同样是占位：StartReconciler 只在 worker 启动时
	// 调用一次，api 进程从不调它，所以这里不需要接 river.Client。
	knowUC := knowledge.NewUsecase(
		kbRepo, docRepo, files,
		knowledge.NewRiverEnqueuer(riverClient),
		retrievalUC,
		knowledge.NewAsyncFileCleaner(riverClient),
		platform.NewRiverScheduler(),
		platform.NewTxManager(pool),
		pool,
	)

	// conversation 拥有会话/消息/事件/长期记忆。ctxmgr 是纯计算的
	// Manager——唯一的外部依赖是 llm.Registry（经 LLMCompressor 间接持有）。
	convRepo := conversation.NewPgRepo()
	memRepo := conversation.NewPgMemoryRepo()
	ctxm := ctxmgr.NewUsecase(ctxmgr.NewLLMCompressor(registry))
	convUC := conversation.NewUsecase(
		convRepo,
		conversation.NewPgLockedWriter(pool),
		registry, llmRepo, ctxm, retrievalUC, memRepo,
		pool,
	)

	// agent 拥有工具、Agent 配置、执行记录。三个内置工具全是 ReadOnly
	// （代码架构设计 §5.9),conversation_search 依赖 convUC 已经装好,
	// 所以工具注册必须排在 convUC 构造之后。
	agentRepo := agent.NewPgRepo()
	toolReg := agent.NewToolRegistry()
	toolReg.Register(agent.NewCalculator())
	toolReg.Register(agent.NewKnowledgeSearch(retrievalUC))
	toolReg.Register(agent.NewConversationSearch(convUC))
	agentUC := agent.NewUsecase(agentRepo, agent.NewPgCheckpointStore(), toolReg, registry, pool)

	// 还有别的模块要装配时，加在这里。
	// 接不上就说明某个包的依赖方向错了——这段代码同时是依赖图的可执行校验。

	// ── HTTP ────────────────────────────────────────────
	r := gin.New()

	// 【关掉尾斜杠自动重定向】默认 true 时，GET /api/v1/knowledge-bases/
	// 会被 gin 的路由层直接回一个 301 + HTML 跳到无斜杠版本。
	// 两个问题：
	//   1. 这个 301 发生在【中间件之前】，所以它没有 X-Request-Id，日志里追不到；
	//   2. 响应体是 HTML，而契约声明这个前缀下的一切都是 JSON / problem+json。
	// 关掉之后带斜杠的路径落到 NoRoute，由 MountSPA 的 isAPIPath 判成
	// 404 Problem——和拼错端点的行为一致。
	//
	// 对前端路由没有影响：/knowledge-bases/ 不是 API 前缀，依旧返回 index.html。
	r.RedirectTrailingSlash = false

	r.Use(
		platform.RequestID(),
		platform.OriginCheck(cfg),
		platform.Recovery(logger),
	)

	srv := api.NewServer(api.Deps{
		Logger:         logger,
		Knowledge:      knowUC,
		LLM:            llmUC,
		Conversation:   convUC,
		Agent:          agentUC,
		MaxUploadBytes: cfg.MaxUploadBytes,
	})

	// 路由不在这里写：RegisterHandlers 由 generated.go 生成，
	// 按 contracts/openapi.yaml 把每个 URL 挂到对应的方法上。
	//
	// 【必须传 ErrorHandler】包装层的参数绑定失败（比如路径里的 uuid 格式不对）
	// 默认会返回 {"msg": ...} + application/json，和契约声明的 Problem 不一致。
	api.RegisterHandlersWithOptions(r, srv, api.GinServerOptions{
		ErrorHandler: api.BindErrorHandler,
	})

	// 内嵌的前端也由这个进程提供。
	// webFS 是 main.go 用 go:embed 嵌进来的前端产物（apps/api/web/）。
	// MountSPA 把它挂在所有未匹配路径上：真实文件直接发，
	// 找不到的返回 index.html（供单页应用的前端路由使用）。
	if err := api.MountSPA(r, webFS); err != nil {
		return fmt.Errorf("mount embedded frontend: %w", err)
	}

	// ── 启动与优雅退出 ──────────────────────────────────
	//
	// 【为什么不用 r.Run】gin 的 Engine.Run 在内部构造一个**局部**的
	// http.Server 再 ListenAndServe，那个 server 既没返回也没存字段，
	// 外部拿不到 → 无法调 Shutdown。所以这里自己造 server。
	//
	// 【绝对不要设 ReadTimeout / WriteTimeout】WriteTimeout 覆盖到整个
	// 响应写完为止，而 SSE 是长响应——设了等于给每次对话加一个硬上限。
	// ReadHeaderTimeout 只管请求头，对 SSE 安全。
	//
	// 【BaseContext 是排空的关键】它让每个请求的 ctx 都挂在 rootCtx 上。
	// 关停时 cancelRoot() 会让所有在途 handler 的 ctx.Done() 立刻触发，
	// 而 sseSink 的 Done() 返回的正是 c.Request.Context().Done()——
	// 这就是「排空 SSE 不需要改 sse.go 一行」的原因。
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return rootCtx },
	}

	logger.Info("listening", "addr", cfg.ListenAddr)

	serveErr := make(chan error, 1)
	go func() {
		// Shutdown / Close 之后 ListenAndServe 返回 ErrServerClosed，
		// 那是正常关停，不是错误——不滤掉的话每次 Ctrl-C 都会打一条
		// 看起来像故障的日志。
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("run http server: %w", err)
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// 监听失败（端口被占用之类）要立刻返回，不能傻等信号。
		return err
	case <-ctx.Done():
	}

	// 【必须先 stop()】ctx 已经取消之后，NotifyContext 仍然占着信号处理器，
	// 不 stop() 的话第二次 Ctrl-C 会被它吃掉——用户就失去了「再按一次
	// 强行退出」这条路，只能去杀进程。
	stop()
	logger.Info("shutdown signal received, draining")

	if err := drain(httpSrv, cancelRoot, apiDrainTimeout, logger); err != nil {
		logger.Warn("graceful shutdown incomplete, connections force-closed", "error", err)
	}

	// drain 返回时监听已经关闭（Shutdown 或 Close 都保证这一点），
	// 所以这个接收一定会到，不会挂住。
	<-serveErr
	return nil
}
