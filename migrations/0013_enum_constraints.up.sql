-- 文本枚举列的 CHECK 约束 + 删掉被唯一约束完全覆盖的索引（issue #120）。
--
-- ── 1. 补上 CHECK，让数据库自己挡住脏值 ───────────────────────────
-- 0002 给 llm_models.kind 加过一条 CHECK，理由写在那个迁移里："应用层校验
-- 漏了的话，这一条兜底"。但同类列里只有它一个享有这条待遇，其余全靠应用层
-- 写对。而枚举列写进脏值的后果【不是报错，是静默的错行为】：
--   * documents.status 落进 'Ready' 这类值 → ProcessDocument 的 switch 掉进
--     default 分支永远返回 ErrConflict，文档既不在 reindexableStatuses 里、
--     也无法重新排队，UI 上永远停在"处理中"（internal/knowledge/usecase.go）。
--   * messages.role 的脏值会被 toLLMMessages 原样喂给模型。
-- 取值清单与代码里的常量表逐字对齐，不另立一份：
--   documents.status              internal/knowledge/document.go 的 Status
--   messages.role                 internal/domain 的 Role
--   messages.status               internal/conversation/model.go 的 MessageStatus
--   tools.side_effect_level       internal/agent/model.go 的 SideEffectLevel
--   agent_runs.status             internal/agent/model.go 的 RunStatus
--   agent_run_steps.type          internal/agent/model.go 的 StepType
--   agent_run_steps.status        internal/agent/model.go 的 StepStatus
-- 那几处逐列写死值（而不是 IN (SELECT …) 之类）是刻意的：这一层要的就是一份
-- 不依赖任何别的东西的独立声明，否则它挡不住"应用层改错了值"这件事。
--
-- 【加约束会校验一遍已有行】ADD CONSTRAINT … CHECK 默认立即验证，拿
-- ACCESS EXCLUSIVE 锁扫一遍表；这几张表在当前规模下都是瞬时的。已实测过
-- 开发库里的现有值全部落在合法集合内（documents.status 只有 ready/failed、
-- agent_runs.status 只有 completed/interrupted 等），所以这条迁移不会因为
-- 历史数据跑不过。不用 NOT VALID / VALIDATE 两步走——那是给"表大到扫不完、
-- 又不能让新写入漏过校验"准备的，现在没有这个前提，多出来的两步只是
-- 让"约束到底生效没有"多一个中间态。
--
-- 约束名沿用 Postgres 对列内联 CHECK 的默认命名（<表>_<列>_check）：这样
-- 按约束名分流的地方（如 internal/platform/pgerr.go 那类代码）将来要引用
-- 它时，名字是可预期的，而不是迁移里随手起的一个。
ALTER TABLE documents
    ADD CONSTRAINT documents_status_check
    CHECK (status IN ('queued', 'processing', 'ready', 'failed'));

ALTER TABLE messages
    ADD CONSTRAINT messages_role_check
    CHECK (role IN ('system', 'user', 'assistant', 'tool'));

ALTER TABLE messages
    ADD CONSTRAINT messages_status_check
    CHECK (status IN ('streaming', 'completed', 'failed'));

ALTER TABLE tools
    ADD CONSTRAINT tools_side_effect_level_check
    CHECK (side_effect_level IN ('READ_ONLY', 'WRITE_IDEMPOTENT', 'WRITE_NON_IDEMPOTENT'));

ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_status_check
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled', 'interrupted'));

ALTER TABLE agent_run_steps
    ADD CONSTRAINT agent_run_steps_type_check
    CHECK (type IN ('llm', 'tool'));

ALTER TABLE agent_run_steps
    ADD CONSTRAINT agent_run_steps_status_check
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'interrupted'));

-- ── 2. 删掉被唯一约束完全覆盖的索引 ───────────────────────────────
-- messages_conversation_sequence_unique 是 UNIQUE (conversation_id, sequence_no)，
-- 它的最左前缀就是 messages_conversation_id_idx 的全部内容——后者被完全覆盖。
-- 所有按会话过滤的查询都同时带 conversation_id（RecentMessages /
-- ListMessagesPage / MessagesAfter / MAX(sequence_no) / 删会话时的级联查找），
-- 走唯一索引的最左前缀即可，删除消息行也一样（唯一索引反而能顺带服务）。
--
-- 留着它的代价是每一次 INSERT 都要多维护一棵 btree，而 messages 是全项目
-- 写入最频繁的表（每轮对话至少 2 行，流式过程中还有 checkpoint 更新）。
-- 这次判断和 0008 删掉那两个被复合索引覆盖的单列索引时完全一致，
-- 只是那一轮漏了这一条。
DROP INDEX IF EXISTS messages_conversation_id_idx;
