-- 回填已有的 chat 模型的「支持工具调用」能力位（issue #38）。
--
-- ── 为什么这条迁移是对的，而不是「擅自改用户的选择」 ─────────────
-- 在 #38 之前，ToolCalling 这一位从来没有被任何生产代码读过（核实过：全仓
-- 只有接口声明、唯一实现和四个测试假实现），Agent 也一直无条件绑工具。
-- 也就是说，升级之前平台在行为上把所有 chat 模型都当成了「支持工具调用」。
--
-- 所以这条 UPDATE 不是新开一个能力，而是把既成事实落成数据。加了门控却
-- 不回填的话，升级那一刻所有带工具的 Agent 会一次性全部不能创建/运行，
-- 而用户什么都没改过——那是升级即坏。
--
-- ── 为什么是幂等的 ───────────────────────────────────────────
-- 判据是「数组里还没有这一位」才追加，重复跑不会把同一个标签加两遍。
--
-- ── 为什么只动 kind = 'chat' ─────────────────────────────────
-- embedding 行的 capabilities 是 {embedding}（见 internal/llm/usecase.go
-- 里构造 embedding 行那一段）。给它加 tool_calling 是没有意义的噪音。
UPDATE llm_models
SET capabilities = capabilities || ARRAY['tool_calling']
WHERE kind = 'chat'
  AND NOT ('tool_calling' = ANY(capabilities));
