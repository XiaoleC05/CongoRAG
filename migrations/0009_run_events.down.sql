-- 回退 0009。
--
-- 【顺序】先删计数器再删事件表没有语义上的强制（两者都是纯新增、互不引用），
-- 但保持与 up 的倒序一致，读的时候不用重新推一遍。两张表都以 agent_runs(id)
-- 为外键，删掉它们不影响 agent_runs 本身。
DROP TABLE IF EXISTS run_counters;
DROP TABLE IF EXISTS run_events;
