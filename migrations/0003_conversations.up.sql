-- 0003_conversations
--
-- M2 RAG 闭环需要的六张表：会话本体（conversations/messages）、
-- 幂等键（idempotency_keys）、用量记账（token_usage）、
-- SSE 事件持久化 + 续传发号器（conversation_events/conversation_counters）。

-- ── 会话 ────────────────────────────────────────────────────────
--
-- knowledge_base_id 可空——这是方案文档没有明确定义、这一轮实现时
-- 补上的决策：一个会话【可选】关联一个知识库。关联了，Send 时会用它
-- 做检索增强；没关联，就是一次不带 RAG 的纯对话。
--
-- 【ON DELETE SET NULL 不是 CASCADE】删除一个知识库不应该删掉引用过它
-- 的历史会话——那些对话记录本身还有价值（用户可能还想回看），只是
-- 之后再发消息不会再检索到已删除知识库的内容。这和 knowledge_base
-- 删除文档时的级联硬删（那是知识库内部的从属关系）是不同的关系性质。
CREATE TABLE conversations (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    title             text        NOT NULL,
    knowledge_base_id uuid        REFERENCES knowledge_bases(id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX conversations_knowledge_base_id_idx ON conversations (knowledge_base_id);

-- ── 消息 ────────────────────────────────────────────────────────
-- sequence_no 是消息在会话内的逻辑顺序，由 PG advisory lock 内分配
--（代码架构设计 §5.8）——锁保证同一会话不并发写，sequence_no 保证顺序
-- 是数据库事实，不是"谁先抢到锁"这件事本身。
--
-- status 三态：streaming（正在流式生成）→ completed / failed。
-- 用户消息落库那一刻直接是 completed（它不经过流式生成这一步）。
--
-- token_usage 是 jsonb 不是两个 int 列：一条消息可能来自不同阶段
--（比如将来的压缩摘要）产生多组用量，用列存不下这种可扩展性；
-- 现在只有 assistant 消息才填它，用户消息这一列是 NULL。
CREATE TABLE messages (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid        NOT NULL
                    REFERENCES conversations(id) ON DELETE CASCADE,
    role            text        NOT NULL, -- system | user | assistant | tool
    content         text        NOT NULL,
    status          text        NOT NULL DEFAULT 'completed', -- streaming | completed | failed
    sequence_no     bigint      NOT NULL,
    token_usage     jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),

    -- 同一会话内序号唯一——这是"顺序是数据库事实"这句话的强制点，
    -- advisory lock 保证的是不并发分配，这条约束保证万一锁被绕过
    -- （代码写错了），数据库自己会拒绝而不是静默接受重复序号。
    CONSTRAINT messages_conversation_sequence_unique UNIQUE (conversation_id, sequence_no)
);

CREATE INDEX messages_conversation_id_idx ON messages (conversation_id);

-- ── 幂等键 ──────────────────────────────────────────────────────
-- 代码架构设计 §9.2：23505 按约束名分流,这张表的主键名
-- （PRIMARY KEY (endpoint, idempotency_key) 默认生成 idempotency_keys_pkey）
-- 被 internal/platform/pgerr.go 的 wrapPgErr 直接引用——改表名或约束名
-- 要同步改那边的 constraintIdempotencyKey 常量。
--
-- resource_type / resource_id 是"这个幂等键最终对应哪个资源"，
-- 命中重复键时用它们查出已创建的资源标识返回给客户端，不重新执行一遍。
CREATE TABLE idempotency_keys (
    endpoint        text        NOT NULL,
    idempotency_key text        NOT NULL,
    resource_type   text        NOT NULL,
    resource_id     uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (endpoint, idempotency_key)
);

-- ── Token 用量 ──────────────────────────────────────────────────
-- provider_id / model_id 记录这次调用花的是哪个 provider 的哪个模型——
-- 换模型之后历史用量不会被覆盖或混淆，GET /api/v1/usage（M5）按它们聚合。
--
-- message_id 可空：不是每一次计量都对应一条已经落库的消息
--（比如未来的摘要压缩、记忆抽取也会消耗 token，但那不是一条"消息"）。
CREATE TABLE token_usage (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id       uuid        NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
    model_id          uuid        NOT NULL REFERENCES llm_models(id) ON DELETE CASCADE,
    message_id        uuid        REFERENCES messages(id) ON DELETE SET NULL,
    kind              text        NOT NULL, -- chat | embedding
    prompt_tokens     int         NOT NULL DEFAULT 0,
    completion_tokens int         NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX token_usage_provider_id_idx ON token_usage (provider_id);

-- ── SSE 事件：会话维度 ──────────────────────────────────────────
-- 【只有会话维度,没有 run 维度】普通聊天不创建 Run（M4-A 才有 Run），
-- 这里只服务 conversation.EventSink 的续传（GET /conversations/{id}/events）。
-- run 维度的 run_events/run_counters 留给 M4-B（代码架构设计 §8.6）。
--
-- payload 是 jsonb：不同 event 类型（token/citation/tool_call/error/done）
-- 形状不同，用一张表存全部类型，靠应用层按 type 字段解释内容。
CREATE TABLE conversation_events (
    conversation_id uuid        NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    event_id        bigint      NOT NULL,
    payload         jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (conversation_id, event_id)
);

-- next_event_id 是"下一个要分配的 id"，从 1 开始——发号逻辑见
-- internal/conversation/postgres.go 的 NextEventID。
--
-- 【id 会因为事务回滚而出现空洞,这是接受的取舍】计数和写消息在同一个
-- 事务里递增，事务回滚时已经分配出去的 id 不会被复用到下一次——
-- 见 docs/sse-protocol.md 的说明。
CREATE TABLE conversation_counters (
    conversation_id uuid   PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    next_event_id   bigint NOT NULL DEFAULT 1
);
