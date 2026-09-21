-- 回退 0008。
--
-- 【顺序与 up 相反】先重建那两个单列索引，再删掉复合索引——反过来的话，
-- 中间那一瞬间外键级联删除没有任何索引可用，而 DROP INDEX 会持有锁。
--
-- 【为什么用 CREATE INDEX 而不是 IF NOT EXISTS】Postgres 的 CREATE INDEX
-- 不支持 IF NOT EXISTS（只有 CREATE INDEX CONCURRENTLY 之外的形式里也没有）。
-- 回退到一个已经存在的索引上会直接报错——那是对的：说明有人手工建过，
-- 值得看一眼，而不是默默放过。
CREATE INDEX documents_knowledge_base_id_idx ON documents (knowledge_base_id);
CREATE INDEX agent_runs_agent_id_idx ON agent_runs (agent_id);

DROP INDEX IF EXISTS documents_kb_created_id_idx;
DROP INDEX IF EXISTS agent_runs_agent_created_id_idx;
