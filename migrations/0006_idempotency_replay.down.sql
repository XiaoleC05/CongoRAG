-- 回退 0006。
--
-- 【顺序与 up 相反】up 里先加列后建索引，down 里先删索引后删列。
--
-- 【这两列是有数据的】回退会丢掉所有已记录的 first_event_id 与
-- request_fingerprint，也就是所有在途的幂等键都失去重放能力。表本身保留
-- （它是 0003 建的，不归这个迁移管），所以回退之后重复提交会退回到
-- 「再生成一遍」的旧行为——不会报错，只是功能消失。
DROP INDEX IF EXISTS idempotency_keys_created_at_idx;

ALTER TABLE idempotency_keys DROP COLUMN IF EXISTS request_fingerprint;
ALTER TABLE idempotency_keys DROP COLUMN IF EXISTS first_event_id;
