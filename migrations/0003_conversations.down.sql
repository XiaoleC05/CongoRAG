-- 0003_conversations 的回滚。
--
-- 顺序和 up 相反：先删引用别的表的，再删被引用的。
DROP TABLE IF EXISTS conversation_counters;
DROP TABLE IF EXISTS conversation_events;
DROP TABLE IF EXISTS token_usage;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS conversations;
