-- 回退 0012。三个索引都是纯新增，删掉即回到 0011 的状态。
-- 顺序与 up 相反只是习惯：它们之间没有依赖，任何顺序都能跑通。
DROP INDEX IF EXISTS token_usage_created_at_idx;
DROP INDEX IF EXISTS token_usage_message_id_idx;
DROP INDEX IF EXISTS token_usage_model_id_idx;
