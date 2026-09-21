-- 幂等键重放（issue #37）。
--
-- idempotency_keys 这张表 0003 就建好了，但一直没有写入者——全仓没有任何
-- INSERT 语句，表是空的。这一条把它接上。
--
-- ── 为什么补 first_event_id ────────────────────────────────────
-- 命中重复键时服务端不重新执行生成，而是把「这一轮已经产生的事件」补发给
-- 客户端。补发需要一个起点，而 conversation_events 里没有任何轮次标记
-- （只有 conversation_id 和 event_id 两列），所以起点必须在预留这个键的
-- 时候就记下来：那一刻该会话已经发到几号了，之后的就是本轮的事件。
--
-- 【不能照旧文档用 after_event_id = 0】event_id 是按会话发号的
-- （docs/sse-protocol.md「事件与续传」），0 意味着把此前每一轮的 token
-- 全部重放一遍，而客户端会把它们拼进同一个正在生成的气泡里。
--
-- DEFAULT 0 只是为了让 ALTER 能加在已有行上。本表自 0003 起从未被写入过，
-- 所以实际上不会有任何一行真的拿到这个默认值。
ALTER TABLE idempotency_keys ADD COLUMN first_event_id bigint NOT NULL DEFAULT 0;

-- ── 为什么补 request_fingerprint ───────────────────────────────
-- 请求正文的 sha256 十六进制。同一个键配不同的正文时必须报错，而不是把
-- 上一轮的回答重放给用户——否则用户新敲的那句话既没落库、也不会报错，
-- 界面上只是旧答案又出现了一遍。这与本项目最忌讳的那类「静默丢数据」
-- 是同一个形状。
ALTER TABLE idempotency_keys ADD COLUMN request_fingerprint text NOT NULL DEFAULT '';

-- ── 为什么加 created_at 索引 ───────────────────────────────────
-- 键有保留窗口（见 internal/conversation/usecase.go 的 idempotencyKeyTTL）：
-- 每次预留时顺手删掉过期的行。没有这个索引，那条 DELETE 会退化成顺序扫描，
-- 表越大每次发消息越慢。
CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);
