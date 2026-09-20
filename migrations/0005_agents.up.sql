-- 0005_agents
--
-- M4-A Agent 基础需要的四张表：工具目录（tools，纯只读种子数据，
-- 给创建 Agent 的表单渲染勾选列表用）、Agent 本体（agents）、
-- 执行记录（agent_runs）、执行步骤（agent_run_steps）。

-- ── 工具目录 ────────────────────────────────────────────────────
-- 【为什么是种子数据表,不是纯 Go 常量】创建 Agent 的表单要展示"当前
-- 有哪些工具可选"——这件事如果只活在 Go 代码里,前端没有端点可查。
-- 真正的工具实现（Invoke 那部分）仍然是 Go 代码（internal/agent/tool_*.go），
-- 这张表只是"目录"，不是"实现"。
CREATE TABLE tools (
    name              text        PRIMARY KEY,
    description       text        NOT NULL,
    side_effect_level text        NOT NULL, -- READ_ONLY | WRITE_IDEMPOTENT | WRITE_NON_IDEMPOTENT
    created_at        timestamptz NOT NULL DEFAULT now()
);

INSERT INTO tools (name, description, side_effect_level) VALUES
    ('calculator', '做基础算术运算（加减乘除），输入两个数字和一个运算符', 'READ_ONLY'),
    ('knowledge_search', '在指定知识库里做向量检索，找出和查询最相关的文档片段', 'READ_ONLY'),
    ('conversation_search', '在指定会话的历史消息里按关键词查找相关内容', 'READ_ONLY');

-- ── Agent 本体 ──────────────────────────────────────────────────
-- 【tool_names 是数组列,不是 agent_tools junction 表】v1.0 的工具集
-- 很小（3 个），Agent 对工具的选择是"这几个名字里选几个"，用数组列
-- 存被选中的工具名，比多一张纯粹为了多对多关系存在的表更直接——
-- 这个规模下建 junction 表的收益（比如查"哪些 Agent 用了这个工具"）
-- 目前没有任何调用点需要。
CREATE TABLE agents (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    instruction text        NOT NULL DEFAULT '', -- 系统提示词,喂给 adk.ChatModelAgentConfig.Instruction
    tool_names  text[]      NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ── 执行记录 ────────────────────────────────────────────────────
-- 【没有 message_id 列】代码架构设计 §5.10 的 Run 结构体原稿带一个
-- MessageID字段,隐含"每次 Agent 执行都从一条会话消息触发"的假设——
-- 但这一轮的契约是独立的 POST /agents/{id}/runs,不经过 conversation
-- 包,没有对应的消息可挂。Agent 执行嵌入聊天流程是本轮范围之外的整合,
-- 这里不为一个不存在的调用点造一个用不上的外键。
--
-- state_snapshot / state_schema_version 这一轮就写(每步执行完更新),
-- 但读它的 Resume 入口是 M4-C 才做——见 internal/agent/port.go 的
-- CheckpointStore 注释。
CREATE TABLE agent_runs (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id             uuid        NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status               text        NOT NULL DEFAULT 'pending', -- pending|running|completed|failed|cancelled|interrupted
    current_step         int         NOT NULL DEFAULT 0,
    input                text        NOT NULL,
    output               text        NOT NULL DEFAULT '',
    state_snapshot       jsonb,
    state_schema_version int         NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agent_runs_agent_id_idx ON agent_runs (agent_id);

-- ── 执行步骤 ────────────────────────────────────────────────────
-- type 只有两种:llm(模型生成这一轮的输出/工具调用请求)、
-- tool(某一次工具调用的执行)。status 五态,没有 cancelled——
-- 取消这个动作发生在 run 维度,不是某一步自己被取消
-- （代码架构设计 §5.10 原话:"cancelled 在 run 上,不在 step 上"）。
CREATE TABLE agent_run_steps (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id      uuid        NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq         int         NOT NULL,
    type        text        NOT NULL, -- llm | tool
    status      text        NOT NULL DEFAULT 'pending', -- pending|running|completed|failed|interrupted
    tool_name   text,
    tool_args   jsonb,
    tool_result jsonb,
    token_usage jsonb,
    latency_ms  int,
    error       text,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT agent_run_steps_run_seq_unique UNIQUE (run_id, seq)
);

CREATE INDEX agent_run_steps_run_id_idx ON agent_run_steps (run_id);
