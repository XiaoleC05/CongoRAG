-- 会话的"最近活动时间"（issue #78）。
--
-- 背景：会话列表要按**最近活动时间**倒序（不是创建时间）。一个三天前建、
-- 刚刚才用过的会话必须排在前面——列表的用处正是"回到刚才那个会话"。
--
-- 而 conversations.updated_at 在此之前只有创建那一刻写过一次（全仓只有
-- CreateConversation 那条 INSERT 会写它），所以直接拿它排序等于按创建时间
-- 排序。这一条把缺口补上，分两部分：
--
--   1. 回填：已经有消息的会话，把它推到最新一条消息的时间。
--   2. 索引：给新的排序键建索引。
--
-- 之后由 internal/conversation 在同一个 advisory lock 的事务里维护它
-- （每次收到消息就 TouchConversation）——见 port.go 里那个方法的注释，
-- 以及 usecase.go 里"为什么必须在同一个事务里"的说明。
--
-- ── 为什么复用 updated_at，而不是新加一列 last_activity_at ──────────
-- 两列会立即产生"哪一列才是真相"的问题，而它们的值在设计上永远相同。
-- 更要紧的是 updated_at 现在**已经在对外暴露**（Conversation 的
-- updatedAt 字段），而它一直在说谎：永远等于 createdAt。让它变成
-- "这一行最后一次因会话活动而变化的时间"，既修好了那一处，也不多一列。
--
-- ── 回填的写法 ────────────────────────────────────────────────
-- GREATEST 而不是直接取 max(created_at)：一个刚建出来、还没发过消息的
-- 会话不该被回填成 NULL（updated_at 是 NOT NULL），也不该被"回填"到一个
-- 比创建时间更早的值。
UPDATE conversations c
   SET updated_at = GREATEST(c.updated_at, m.last_message_at)
  FROM (SELECT conversation_id, max(created_at) AS last_message_at
          FROM messages
         GROUP BY conversation_id) m
 WHERE m.conversation_id = c.id;

-- 列表的排序键是 (updated_at DESC, id DESC)，游标谓词是
-- (updated_at, id) < (上一页的最后一行)——索引的列顺序与方向必须和它们
-- 逐字对上，否则 Postgres 会退化成"索引扫 + Sort"，分页的意义就没了。
-- （理由与 migrations/0008 完全一致。）
CREATE INDEX conversations_updated_at_id_idx
    ON conversations (updated_at DESC, id DESC);
