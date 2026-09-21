-- 列表分页要用的索引（issue #45）。
--
-- 文档列表与 Agent 运行历史都从「按 created_at 倒序、无上界」改成了
-- keyset 分页，翻页谓词是 `(created_at, id) < (上一页最后一行)`。要让
-- Postgres 走索引范围扫描而不是「索引扫 + Sort」，索引的列顺序与方向
-- 必须和 ORDER BY 逐字对上：过滤列在前、排序键在后、方向一致。
--
-- ── 为什么带上 id ──────────────────────────────────────────
-- 同一毫秒创建的两行 created_at 完全相同（时间戳由 Go 的 time.Now() 生成，
-- 批量操作里 resolve 得没那么细）。只按 created_at 分页时，这两行会稳定地
-- 多出现一次或少出现一次——不报错，只是用户看到重复的文档。
-- 加上 id 之后 (created_at, id) 是严格全序，翻页不重不漏。
--
-- ── 为什么删掉被取代的单列索引 ────────────────────────────
-- 新索引的最左列就是那两个单列索引的唯一列，它们被完全覆盖。留着只会让
-- 每一次 INSERT 多维护一棵索引树。
--
-- 【外键级联删除的查找也走新索引】documents.knowledge_base_id 与
-- agent_runs.agent_id 上的 ON DELETE CASCADE 查的是同一列，新索引的最左
-- 前缀就能服务它。
CREATE INDEX documents_kb_created_id_idx
    ON documents (knowledge_base_id, created_at DESC, id DESC);

CREATE INDEX agent_runs_agent_created_id_idx
    ON agent_runs (agent_id, created_at DESC, id DESC);

DROP INDEX IF EXISTS documents_knowledge_base_id_idx;
DROP INDEX IF EXISTS agent_runs_agent_id_idx;
