-- 工具副作用的唯一账本（issue #63）。
--
-- 项目文档 §9.5 要求给每个 Tool 执行用独立事务提交一行带唯一约束的日志：
--
--   INSERT INTO tool_effect_log(step_id, effect_key) VALUES ($1, $2);
--   -- UNIQUE (step_id, effect_key)
--
-- ── 这张表是 resume 正确性的【裁判】，不是审计日志 ──────────────
-- 判读方向文档写得很清楚，而且特意提醒过别搞反：
--
--   * 看到唯一冲突 23505 = 工具被重复执行了 = resume 没生效  ❌
--   * 无冲突             = 正确跳过了已完成的步骤             ✅
--
-- 也就是说：恢复路径在重放某一步之前先看这里有没有行；有行就说明这个
-- 副作用已经发生过了，跳过重放。实现顺序也照文档来——先在没写 resume
-- 时让这个测试看到 23505，再写 resume 让它消失。
--
-- 文档最后那句是这张表存在的全部理由：
-- **一个会静默重复执行的 resume，比没有 resume 更糟。**
--
-- ── effect_key 是什么 ───────────────────────────────────────
-- 一步的工具效果指纹（工具名 + **规范化之后**的参数 + sha256，见
-- internal/agent 的 EffectKey）。用 (step_id, effect_key) 而不是只用
-- step_id 做键，是因为"一步"在概念上不保证只产生一个副作用；
-- 现在三个内置工具都是每步一次调用，但把这一步的粒度写进键里，
-- 将来真出现一步多副作用时不需要改表。
--
-- ⚠️ **"规范化"这三个字不能省。** 同一个效果会被算两次：执行时参数来自
-- Eino 的事件（原始字节），恢复时从 `agent_run_steps.tool_args` 读回来
-- ——而那一列是 jsonb，Postgres 会重写它（冒号/逗号后加空格、键按长度重排）。
-- 直接哈希原始字节的话两侧必然不同，判据静默失效、工具被重复执行，
-- 而唯一约束也拦不住（键不同）。细则见 internal/agent/model.go 的 EffectKey。
--
-- ── 为什么带外键，而 Agent 的其它表都不带 ─────────────────────
-- 它不是"记录"，是"约束"：step 行不存在时这条账本毫无意义，
-- 而 step 行会被 agent_run_steps 的级联删除带走（run 删了，step 删了，
-- 账本也该跟着走）。没有外键的话，resume 会读到指向不存在步骤的账本行，
-- 判读方向立刻失效。
CREATE TABLE tool_effect_log (
    step_id    uuid        NOT NULL REFERENCES agent_run_steps(id) ON DELETE CASCADE,
    effect_key text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT tool_effect_log_step_effect_unique UNIQUE (step_id, effect_key)
);
