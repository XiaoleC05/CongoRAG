-- run 维度的事件流（issue #54）。
--
-- migrations/0003_conversations.up.sql 在 conversation_events 上面留了一句挂账：
--
--   -- run 维度的 run_events/run_counters 留给 M4-B（代码架构设计 §8.6）。
--
-- 这一条把那个挂账还掉。
--
-- ── 为什么是两张并存的表，不是把会话那张扩一列 ────────────────
-- v3.0 的断线续传是【会话维度】的：GET /api/v1/conversations/{id}/events 按
-- after_event_id 补发会话事件。Agent 的一次运行不共享会话的编号空间——
-- event_id 按会话发号，run 的事件混在里面就没有边界，「这次 run 推到哪了」
-- 在协议层答不出来。§8.6 定的方案是两张表并存、各自独立编号，不搬迁、
-- 不合并。所以这张表是照 conversation_events 的形状新建的，
-- conversation_events 保持不动，普通聊天路径完全不受影响。
--
-- ── 为什么 event_id 从 1 重新起跳（不是全局自增，也不是接着会话的号）──
-- 与 conversation_counters 同一个理由（见 0003 的注释）：发号维度跟着
-- 流的所有者走。run 自己的号从 1 开始，好处是「这个 run 一共发了多少条
-- 事件」= 最大的 event_id（除去回滚留下的空洞），且补发时
-- after_event_id=0 天然表示"从头补发整个 run"。
--
-- ── 空洞同样是接受的取舍 ────────────────────────────────────
-- 理由与 conversation_counters 逐字相同：计数器递增和它所属的事务一起
-- 回滚时，已分配的号不复用。客户端只能假设单调递增，不能假设连续。
CREATE TABLE run_events (
    run_id     uuid        NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    event_id   bigint      NOT NULL,
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (run_id, event_id)
);

-- next_event_id 是"下一个要分配的 id"，从 1 开始——发号逻辑见
-- internal/agent/postgres.go 的 NextRunEventID。
CREATE TABLE run_counters (
    run_id        uuid   PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
    next_event_id bigint NOT NULL DEFAULT 1
);
