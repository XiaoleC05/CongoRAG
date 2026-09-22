-- 回退 0014。纯新增索引，删掉即回到 0013 的状态。
DROP INDEX IF EXISTS run_events_created_at_idx;
