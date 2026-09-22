-- 回退 0013。
--
-- 【按 up 的逆序写】up 是先加约束、后删索引，回退先把索引建回来、再拆约束，
-- 逐条对着看时两边顺序是对称的。
--
-- 【重建索引用 CREATE INDEX，不加 IF NOT EXISTS】Postgres 的 CREATE INDEX
-- 回退到一个已经存在的同名索引上应该直接报错——那说明有人手工建过，
-- 值得看一眼，而不是默默放过（理由同 0008 的 down）。
CREATE INDEX messages_conversation_id_idx ON messages (conversation_id);

ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_status_check;
ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_runs      DROP CONSTRAINT IF EXISTS agent_runs_status_check;
ALTER TABLE tools           DROP CONSTRAINT IF EXISTS tools_side_effect_level_check;
ALTER TABLE messages        DROP CONSTRAINT IF EXISTS messages_status_check;
ALTER TABLE messages        DROP CONSTRAINT IF EXISTS messages_role_check;
ALTER TABLE documents       DROP CONSTRAINT IF EXISTS documents_status_check;
