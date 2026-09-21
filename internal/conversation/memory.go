// memory.go 实现代码架构设计 §5.6 的长期记忆，和 §6.4 的滚动摘要维护。
//
// 【为什么摘要维护和长期记忆放在同一个文件】两者都是"从历史对话里提炼
// 出更紧凑的表示,定期跑,不是每轮同步"这同一类工作——开发文档 §6.4:
// "长期记忆的读写时机要显式设计:何时抽取偏好落库(推荐每 N 轮由 worker
// 跑,不是每轮同步)……Summary 管刚才聊了什么,Memory 管这个用户长期的
// 偏好"。两者分工不同,但触发时机、运行方式(周期任务)是同一套机制,
// 拆成两个文件只会让"这两件事本质上是同类工作"这一点变得不明显。
package conversation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// defaultMemoryScope 是 ExtractPreferences 写入、RetrieveMemory 检索时
// 统一使用的 scope 值。
//
// 【为什么只有一个 scope】方案的部署边界是本地单机应用,没有多用户预期
// （开发文档 §1）——scope 这一列因此现在只用来做"这一类记忆"的分类,
// 不是"哪个用户"的分类。多用户/多租户场景出现之前,不提前设计一个
// 用不上的 scope 划分方案。
const defaultMemoryScope = "user_preference"

// memoryTopK 是 RetrieveMemory 每次检索取的条数上限——和
// retrieval.defaultTopK 同样的量级、同样的理由:长期记忆是个性化增强,
// 不是回答问题的直接证据,不需要取很多条。
const memoryTopK = 3

// summaryTriggerMessages 是 MaintainSummary 判断"要不要重新压缩"的门槛:
// 自上次摘要覆盖到的位置之后,新增了这么多条消息才触发一次压缩。
//
// 【为什么不是"每轮同步压"】开发文档 §6.4 明确推荐"每 N 轮由 worker 跑,
// 不是每轮同步"——理由是压缩本身要调一次 LLM,同步做会拖慢每一次
// Send 的响应时间,而摘要这件事本身不需要绝对实时(差几轮不影响可用性,
// ctxmgr 的预算阶梯在摘要滞后的情况下,只是把还没被摘要吸收的历史
// 也当作 RecentMessages 一起塞进可压缩区,不会丢信息)。
const summaryTriggerMessages = 20

// summaryMaintenanceInterval 是摘要维护周期任务的运行间隔。
const summaryMaintenanceInterval = 5 * time.Minute

// preferenceExtractionInterval 是长期记忆抽取周期任务的运行间隔。
//
// 【为什么和摘要维护是两个独立周期,不合并成一次扫描】二者的触发门槛
// 不同(summaryTriggerMessages vs preferenceExtractionMessages)、失败
// 互相独立(摘要压缩失败不该连累偏好抽取,反之亦然)、未来各自的运行
// 频率可能需要单独调整——合并成一次扫描省下的只是 ListConversationIDs
// 这一次查询,不值得为此让两件独立的事互相耦合。
const preferenceExtractionInterval = 10 * time.Minute

// preferenceExtractionMessages 是 ExtractPreferences 判断"要不要抽取"的
// 门槛,和 summaryTriggerMessages 同样的道理但独立计数——偏好抽取比
// 摘要压缩更"贵"(需要 LLM 判断"这段话有没有透露长期偏好",不是单纯
// 压缩文本),门槛设得更高,减少不必要的 LLM 调用。
const preferenceExtractionMessages = 40

// memoryEmbeddingInterval 是长期记忆补算向量的周期任务间隔（issue #39）。
//
// 【为什么和偏好抽取同一个量级】它平时什么都不做（一条待补的记忆都没有），
// 而真正有事可做只发生在换 embedding 模型之后。10 分钟意味着换完模型到
// 记忆重新可用最多等一个 tick——这个延迟对"长期记忆"这种增强能力完全可以接受，
// 而更短的间隔会让一条空转的查询每几分钟打一次库。
const memoryEmbeddingInterval = 10 * time.Minute

// memoryReembedBatch 是单次补算最多处理多少条记忆。
//
// 【为什么要分批】换模型之后可能一次性有几千条待补。一批一次 embedding 请求
// 也顺带给了这个任务一个自然的检查点：被关停截断时，已处理的那批已经写回，
// 下一次 tick 从剩下的继续。
const memoryReembedBatch = 256

// ────────────────────────────────────────────────────────────────
// 摘要（Summary）：管"刚才聊了什么"
// ────────────────────────────────────────────────────────────────

// RetrieveSummary 取一个会话当前的摘要文本和它覆盖到的 sequence_no。
// 从没压缩过时返回 ("", 0, nil)——0 让调用方（send）的 RecentMessages
// 查询退化成"取全部历史"，和没有摘要机制之前的行为完全一致。
func (u *Usecase) RetrieveSummary(ctx context.Context, convID uuid.UUID) (summary string, coveredUntil int64, err error) {
	s, err := u.repo.GetSummary(ctx, u.db, convID)
	if errors.Is(err, platform.ErrNotFound) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("get summary: %w", err)
	}
	return s.Summary, s.CoveredUntilSequenceNo, nil
}

// summarizePrompt 复用 ctxmgr.LLMCompressor 同一个思路的独立提示词——
// 【为什么不直接调 ctxmgr.Compressor】那个接口的输入是 []ctxmgr.Item,
// 输出目标是"压到某个 token 数以内"(服务的是请求路径实时的预算阶梯);
// 这里的输入是 []*Message,目标是"把新消息合并进已有摘要"(服务的是
// 后台周期任务,没有 token 预算的概念,只有"话说清楚"的概念)。
// 两者的输入类型、目标函数都不同,共用一个方法会让 Compressor 的接口
// 承担两种不相关的语义。
const summarizePrompt = "你会看到一份已有的对话摘要（可能为空）和一批新的对话消息。" +
	"请把新消息合并进已有摘要，产出一份更新后的摘要，保留关键事实、决定和用户表达过的偏好，" +
	"去掉寒暄和重复内容。直接输出更新后的摘要正文，不要加任何前缀说明。"

// MaintainSummary 是滚动摘要维护的核心逻辑,由 StartMemoryMaintenance
// 注册的周期任务调用,也可以在测试里直接调用(不需要真的等到周期触发)。
//
// 【为什么门槛不够时直接返回 nil,不是报错】"这个会话新消息还不够多,
// 这一轮不用处理"是正常状态,不是异常——和 knowledge.Usecase 的孤儿对账
// job 遇到"没有候选文件"时直接返回 nil 是同一个模式。
func (u *Usecase) MaintainSummary(ctx context.Context, convID uuid.UUID) error {
	_, coveredUntil, err := u.RetrieveSummary(ctx, convID)
	if err != nil {
		return fmt.Errorf("retrieve summary of conversation %s: %w", convID, err)
	}

	latest, err := u.repo.LatestSequenceNo(ctx, u.db, convID)
	if err != nil {
		return fmt.Errorf("latest sequence_no of conversation %s: %w", convID, err)
	}
	// latest 只数已定稿的消息（postgres.go 里 SQL 的 status = 'completed'
	// 断言）：还在生成中的占位行不该把这条门控顶过去。
	if latest-coveredUntil < summaryTriggerMessages {
		return nil
	}

	// 一次最多处理 summaryTriggerMessages 条——和第 5.6 节的
	// MessagesAfter 文档注释解释的一致:一轮处理不完时,下一轮周期任务
	// 会接着从新的 covered_until_sequence_no 继续,不会遗漏。
	msgs, err := u.repo.MessagesAfter(ctx, u.db, convID, coveredUntil, summaryTriggerMessages)
	if err != nil {
		return fmt.Errorf("load messages after %d of conversation %s: %w", coveredUntil, convID, err)
	}

	// 截到"最后一条已定稿的消息"为止：批次尾部可能还是生成中的行，
	// 而水位线一旦跨过它，生成结束后才写进去的正文就既不在摘要里、
	// 又被 RecentMessages（sequence_no > covered_until）排除——这条已完成
	// 的回答从此对模型永久不可见，无错误、无日志、无重试。
	//
	// 【为什么在这里截断，而不是给 MessagesAfter 加 status 断言】客户端
	// 断开或上游中途报错时留下的 failed 行是助手已经生成的正文（客户端
	// 不会再补写完成态），按状态整批过滤会把它从摘要里也删掉，而水位线
	// 照样越过它——等于换个触发方式制造同一个 bug。截断只丢掉尾部尚未
	// 定稿的行，夹在中间那些已经冻结的行照常被吸收；被截掉的行留在
	// 水位线之后，下一次触发时会被正常吸收，不是丢弃。
	lastCompleted := -1
	for i, m := range msgs {
		if m.Status == MsgCompleted {
			lastCompleted = i
		}
	}
	if lastCompleted < 0 {
		return nil
	}
	msgs = msgs[:lastCompleted+1]

	priorSummary, _, err := u.RetrieveSummary(ctx, convID)
	if err != nil {
		return fmt.Errorf("retrieve prior summary of conversation %s: %w", convID, err)
	}

	chatModelID, err := u.registry.ActiveModelID(ctx, llm.KindChat)
	if err != nil {
		return fmt.Errorf("resolve active chat model for summarization: %w", err)
	}
	chatModel, err := u.registry.Chat(ctx, chatModelID)
	if err != nil {
		return fmt.Errorf("get chat model for summarization: %w", err)
	}

	var body strings.Builder
	body.WriteString("已有摘要：\n")
	if priorSummary == "" {
		body.WriteString("（无）\n")
	} else {
		body.WriteString(priorSummary + "\n")
	}
	body.WriteString("\n新的对话消息：\n")
	for _, m := range msgs {
		body.WriteString(string(m.Role) + ": " + m.Content + "\n")
	}

	resp, err := chatModel.Generate(ctx, []llm.Message{
		{Role: domain.RoleSystem, Content: summarizePrompt},
		{Role: domain.RoleUser, Content: body.String()},
	})
	if err != nil {
		return fmt.Errorf("generate updated summary for conversation %s: %w", convID, err)
	}

	// 空 completion 当作失败，不能落库：summary 列是 text NOT NULL，空串
	// 满足约束，写进去就是一次"成功的"整体替换——旧摘要被空串覆盖、水位线
	// 照常前移，被它覆盖的那段历史从此既不在摘要里也不在 RecentMessages
	// 里，而旧摘要的文本已经找不回来。返回 error 让 maintainAllSummaries
	// 记下这一轮失败，摘要与水位线都保持原样，下一个 tick 再试（和
	// ExtractPreferences 里"模型没产出行就不写记忆"是同一个判断，区别只是
	// 那里的空结果无害、这里的空结果会覆盖存储）。
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("chat model returned an empty summary for conversation %s: %w", convID, platform.ErrUpstream)
	}

	// 水位线取自"真正被吸收进摘要的最后一条"——上面的截断保证了它是一条
	// 已定稿的消息，不是批次返回的最后一行。
	newCoveredUntil := msgs[len(msgs)-1].SequenceNo
	if err := u.repo.UpsertSummary(ctx, u.db, &Summary{
		ConversationID: convID, Summary: resp.Content,
		CoveredUntilSequenceNo: newCoveredUntil, UpdatedAt: time.Now(),
	}); err != nil {
		return fmt.Errorf("upsert summary of conversation %s: %w", convID, err)
	}
	return nil
}

// ────────────────────────────────────────────────────────────────
// 长期记忆（Memory）：管"这个用户长期的偏好/事实"
// ────────────────────────────────────────────────────────────────

// RetrieveMemory 按相关度检索长期记忆,供 send() 组装进 ctxmgr.Request。
func (u *Usecase) RetrieveMemory(ctx context.Context, req domain.MemoryRequest) ([]domain.Memory, error) {
	k := req.K
	if k <= 0 {
		k = memoryTopK
	}

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
		return nil, fmt.Errorf("embed memory query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for 1 query text: %w", len(vecs), platform.ErrUpstream)
	}

	memories, err := u.memRepo.SearchByRelevance(ctx, u.db, domain.MemoryRequest{Text: req.Text, K: k}, vecs[0], model.ModelID)
	if err != nil {
		return nil, fmt.Errorf("search memories by relevance: %w", err)
	}
	return memories, nil
}

// extractPrompt 要求模型只在真的看到长期偏好/事实时才输出内容——
// 大多数对话轮次里用户不会说任何值得长期记住的话,模型必须能识别
// "这一批消息里什么都不用记"这种情况,而不是每次都硬造一条记忆出来。
const extractPrompt = "分析下面这批对话消息，找出用户明确表达的、值得长期记住的偏好或事实" +
	"（比如「我偏好简洁的回答」「我是 Go 开发者」）。" +
	"每找到一条，单独一行输出，不要编号、不要加任何解释。" +
	"如果这批消息里没有任何值得长期记住的内容，输出一个空字符串，不要编造。"

// ExtractPreferences 从最近一批消息里抽取长期偏好,写入 memories 表。
// 输入是 []llm.Message（线路类型,不是 []Message）——和技术方案 §5.6
// 定义的签名一致:调用方只需要文本内容,不需要数据库那些字段。
func (u *Usecase) ExtractPreferences(ctx context.Context, convID uuid.UUID, recent []llm.Message) ([]domain.Memory, error) {
	if len(recent) == 0 {
		return nil, nil
	}

	chatModelID, err := u.registry.ActiveModelID(ctx, llm.KindChat)
	if err != nil {
		return nil, fmt.Errorf("resolve active chat model for preference extraction: %w", err)
	}
	chatModel, err := u.registry.Chat(ctx, chatModelID)
	if err != nil {
		return nil, fmt.Errorf("get chat model for preference extraction: %w", err)
	}

	var body strings.Builder
	for _, m := range recent {
		body.WriteString(string(m.Role) + ": " + m.Content + "\n")
	}

	resp, err := chatModel.Generate(ctx, []llm.Message{
		{Role: domain.RoleSystem, Content: extractPrompt},
		{Role: domain.RoleUser, Content: body.String()},
	})
	if err != nil {
		return nil, fmt.Errorf("generate preference extraction for conversation %s: %w", convID, err)
	}

	var found []string
	for _, line := range strings.Split(resp.Content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			found = append(found, line)
		}
	}
	if len(found) == 0 {
		return nil, nil
	}

	model, err := u.activeEmbeddingModel(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("resolve active embedding model: %w", err)
	}
	embedder, err := u.registry.Embedder(ctx, model.ID.String())
	if err != nil {
		return nil, fmt.Errorf("get embedder for model %s: %w", model.ID, err)
	}
	vecs, err := embedder.Embed(ctx, found)
	if err != nil {
		return nil, fmt.Errorf("embed %d extracted preferences: %w", len(found), err)
	}
	if len(vecs) != len(found) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d preferences: %w",
			len(vecs), len(found), platform.ErrUpstream)
	}

	out := make([]domain.Memory, len(found))
	for i, content := range found {
		m := domain.Memory{
			ID: uuid.New(), Scope: defaultMemoryScope, Content: content,
			Metadata: map[string]any{"conversation_id": convID.String()},
		}
		if err := u.memRepo.Insert(ctx, u.db, &m, vecs[i], model.ModelID); err != nil {
			return nil, fmt.Errorf("insert extracted preference %d/%d: %w", i+1, len(found), err)
		}
		out[i] = m
	}
	return out, nil
}

// reembedAllMemories 给一批「向量不是当前生效模型生成的」记忆补算向量
// （issue #39）。
//
// 【它为什么存在】换 embedding 模型时，memories.embedding 会被 api 的切换
// 事务清空——那一步是必需的（不同模型的向量空间不通用，留着它们检索会返回
// 语义上错误的结果）。但 memories 不能像文档那样在同一个事务里重新排队：
// 它的向量由这条周期任务产生，重建的成本与时机和文档不同，而且它只有一张
// 表、不需要 River 那种按份重试的粒度。
//
// 【一次一批，不是一次全量】见 memoryReembedBatch 的注释。剩下的留给下一次
// tick，不会永久遗漏——ListNeedingEmbedding 的判据是"当前状态"，处理过的
// 行自动不再入选。
//
// 【没有 embedding 模型时安静返回】引导还没做、或者用户清空了配置，这都不该
// 被记成周期任务失败——那会刷满日志而没有任何可操作的信息。
func (u *Usecase) reembedAllMemories(ctx context.Context) error {
	model, err := u.activeEmbeddingModel(ctx, u.db)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("resolve active embedding model for memory reindex: %w", err)
	}

	pending, err := u.memRepo.ListNeedingEmbedding(ctx, u.db, model.ModelID, memoryReembedBatch)
	if err != nil {
		return fmt.Errorf("list memories needing embedding: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	// 和本文件其余周期任务同一个取舍：ctx 被取消（关停、任务超时）时
	// 当成失败上报，而不是静默记成 completed——否则一轮被截断的重建
	// 看起来就像"做完了，只是没东西可做"。
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memory reindex interrupted before embedding (context done): %w", err)
	}

	embedder, err := u.registry.Embedder(ctx, model.ID.String())
	if err != nil {
		return fmt.Errorf("get embedding model for memory reindex: %w", err)
	}

	contents := make([]string, len(pending))
	for i, m := range pending {
		contents[i] = m.Content
	}
	vecs, err := embedder.Embed(ctx, contents)
	if err != nil {
		return fmt.Errorf("embed %d memories for reindex: %w", len(pending), err)
	}
	if len(vecs) != len(pending) {
		return fmt.Errorf("embedder returned %d vectors for %d memories: %w",
			len(vecs), len(pending), platform.ErrUpstream)
	}

	for i, m := range pending {
		if err := u.memRepo.UpdateEmbedding(ctx, u.db, m.ID, vecs[i], model.ModelID); err != nil {
			return fmt.Errorf("update reindexed memory %d/%d: %w", i+1, len(pending), err)
		}
	}
	return nil
}

// activeEmbeddingModel 找出当前配置的 embedding 模型——和
// retrieval.Usecase.activeEmbeddingModel 是同一段逻辑的独立副本,不是
// 共用一个函数:两个包不允许互相依赖对方的私有实现细节(依赖图规则),
// "当前是哪个模型"这条判据本身的定义处仍然只有 llm.LatestByKind 一份。
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

// ────────────────────────────────────────────────────────────────
// 周期任务注册
// ────────────────────────────────────────────────────────────────

// StartMemoryMaintenance 注册摘要滚动压缩和长期偏好抽取两个周期任务,
// worker 进程启动时调用一次——和 knowledge.Usecase.StartReconciler
// 同样的模式(platform.PeriodicScheduler,不认识 River 的存在)。
func (u *Usecase) StartMemoryMaintenance(ctx context.Context, sched platform.PeriodicScheduler) {
	sched.RegisterPeriodic("conversation-summary", summaryMaintenanceInterval, func(ctx context.Context) error {
		return u.maintainAllSummaries(ctx)
	})
	sched.RegisterPeriodic("conversation-preferences", preferenceExtractionInterval, func(ctx context.Context) error {
		return u.extractAllPreferences(ctx)
	})
	// 【换 embedding 模型之后 memories 的自愈路径（issue #39）】
	// 清空在 api 的切换事务里完成（与 document_chunks 共用同一段 SQL），
	// 重建在这里。两个进程之间不需要协调——api 只负责"把不属于新模型的
	// 向量清掉"，worker 的下一次 tick 会发现它们需要重算。
	sched.RegisterPeriodic("conversation-memory-embeddings", memoryEmbeddingInterval, func(ctx context.Context) error {
		return u.reembedAllMemories(ctx)
	})
}

// maintainAllSummaries 遍历全部会话,对每一个调用 MaintainSummary。
//
// 【单个会话失败不该拖垮整轮】和 asyncFileCleaner.Schedule 里"删不掉
// 只是浪费一点磁盘空间,不是数据错误"同样的取舍——一个会话的摘要这一轮
// 没更新成功(比如正好那个会话的 chat 模型调用超时),不该导致其它
// 会话这一轮也不被处理,下一轮还会再试。
func (u *Usecase) maintainAllSummaries(ctx context.Context) error {
	ids, err := u.repo.ListConversationIDs(ctx, u.db)
	if err != nil {
		return fmt.Errorf("list conversation ids for summary maintenance: %w", err)
	}
	for _, id := range ids {
		if err := u.MaintainSummary(ctx, id); err != nil {
			slog.Default().Error("failed to maintain conversation summary",
				"conversation_id", id, "error", err)
		}
	}
	// 单个会话失败可以容忍,ctx 被取消不行:那说明这一轮是被 job 超时
	// 截断的,排在后面的会话根本轮不到。返回 ctx.Err() 让 River 把这次
	// job 记为失败/重试——一直 return nil 的话,被截断的一轮和完整跑完的
	// 一轮在 river_job 里都是 completed,长期记忆的构建被静默饿死。
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("summary maintenance interrupted (context done): %w", err)
	}
	return nil
}

// extractAllPreferences 遍历全部会话,对达到抽取门槛的会话调用
// ExtractPreferences。
//
// 【门槛判断为什么在这里,不在 ExtractPreferences 内部】ExtractPreferences
// 的签名（技术方案 §5.6）接收的是调用方已经选好的 []llm.Message,它不
// 认识"这个会话上一次抽取到哪条为止"这个进度概念——那个进度追踪是
// 周期任务自己的调度逻辑,和"给一批消息抽偏好"这个纯操作分开职责。
// 这里复用 conversation_summaries 表的 covered_until_sequence_no 语义
// 不合适(那张表是给 Summary 用的),所以这里用 LatestSequenceNo 和一个
// 更粗的门槛（preferenceExtractionMessages）做近似判断,不追求精确的
// "只抽取没抽取过的那一段"——重复抽取到同一条已经提过的偏好，后果只是
// memories 表里出现内容相近的两行，不是错误，下一步的手动去重/合并
// 留给 M5 的运维工具做,不在这一轮的范围内。
func (u *Usecase) extractAllPreferences(ctx context.Context) error {
	ids, err := u.repo.ListConversationIDs(ctx, u.db)
	if err != nil {
		return fmt.Errorf("list conversation ids for preference extraction: %w", err)
	}
	for _, id := range ids {
		latest, err := u.repo.LatestSequenceNo(ctx, u.db, id)
		if err != nil {
			slog.Default().Error("failed to read latest sequence_no", "conversation_id", id, "error", err)
			continue
		}
		if latest < preferenceExtractionMessages {
			continue
		}
		// beforeSequenceNo 传 0：偏好抽取跑在周期任务里，没有"本轮"这个概念，
		// 上界就是当前最新的一条，不需要排除任何东西。
		recent, err := u.repo.RecentMessages(ctx, u.db, id, 0, 0, preferenceExtractionMessages)
		if err != nil {
			slog.Default().Error("failed to load recent messages for preference extraction",
				"conversation_id", id, "error", err)
			continue
		}
		if _, err := u.ExtractPreferences(ctx, id, toLLMMessages(recent)); err != nil {
			slog.Default().Error("failed to extract preferences", "conversation_id", id, "error", err)
		}
	}
	// 和 maintainAllSummaries 同样的理由:ctx 被取消说明这一轮被 job 超时
	// 截断了,剩下的会话根本没扫到。必须上报失败,否则 River 把截断的一轮
	// 记成 completed,偏好抽取是否真的扫完了从 job 状态上看不出来。
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("preference extraction interrupted (context done): %w", err)
	}
	return nil
}
