# ADR-010：提示词进注册表，成为可审计资产

- 状态：已采纳
- 日期：2026-11-14
- 相关：`internal/prompt/prompt.go`、`apps/api/internal/api/handler_prompt.go`、
  `contracts/openapi.yaml`、`docs/prompt-audit.md`

## 背景

v6.2.0 之前，提示词是散落在 6 个文件里的字符串字面量：

| 位置 | 内容 |
| --- | --- |
| `internal/conversation/usecase.go` | 科研问答系统提示 |
| `internal/ctxmgr/compressor.go` | 历史压缩提示与 token 预算约束 |
| `internal/llm/usecase.go` | 论文翻译系统提示（temperature=0.1） |
| `apps/api/internal/api/handler_research.go` | 写作、对比、综述、图注 |
| `apps/api/internal/api/handler_knowledge.go` | 版本差异 |

它们没有版本、没有内容哈希、没有变更记录。改一句话不会留下痕迹——而提示词
就是产品行为：评审与决赛答辩要求"Prompt 审计记录"，`docs/prompt-audit.md`
此前只能靠人工抄写，无法回答"线上跑的这份提示词与仓库里这份是不是同一份"。

同时发现一个真实缺陷：调用方传的 `WithTemperature(0.1)` 被 Eino 适配器用
`_ = opts` 丢弃（`internal/llm/eino.go`），文档里写的采样参数与实际运行不一致。
提示词审计要求"记录的参数 = 实际传给模型的参数"，所以这条必须先修。

## 决策

**新建叶子包 `internal/prompt` 作为提示词的唯一真相，生产代码只通过它取文本。**

1. **版本化**：每条提示词有稳定 key（如 `chat.system`）、标题、说明、变量声明、
   代码位置；每个版本有完整文本、SHA-256、变更原因、创建/生效时间、采样参数与
   （可选）可复现指标。生效版本必须是最后一个已评审版本。
2. **只读 API**：`GET /api/v1/prompts` 返回完整注册表（含每个版本的全文与哈希），
   前端新增「Prompt 审计」页面。**不提供写接口**——允许运行时改会让
   "这条回答是哪版提示词生成的"无法追溯。
3. **启动校验**：api 装配根调用 `prompt.Validate()`，变量漏声明、Active 指错、
   key 重名、代码位置缺失都会让进程拒绝启动（与数据库连不上同一条原则）。
4. **调用方改造**：conversation / ctxmgr / llm / 两个 handler 的内联字面量全部
   改为 `prompt.Render`/`prompt.MustRender`；架构守卫（`internal/arch_test.go`）
   新增 llm→prompt、ctxmgr→prompt、conversation→prompt 三条边，prompt 无出边。
5. **修复采样参数透传**：Eino 适配器用 `WithExtraFields({temperature})` 与
   `WithMaxCompletionTokens` 把 `CallOption` 真正传下去；`resolveCallConfig`
   单独成函数并补单测。

## 理由

- **提示词是代码，不是配置**：版本化管理让它进 git diff、可 review、可用评测集
  对比后再切换，而不是在数据库里被谁改过都查不到。
- **哈希是可审计性的最小单位**：界面上显示的哈希由服务端按内容现算（转换时算、
  不落库），避免"改了文本忘了更新哈希"这类静默不一致。
- **叶子包 + 架构守卫**：prompt 不依赖任何内部包，所有消费者单向依赖它；
  新增包必须先在 `arch_test.go` 的边表登记，否则测试红。

## 后果

- **好处**：评审/答辩能在一个页面里看到每条提示词的版本时间线、变更原因、
  完整文本、SHA-256、采样参数、代码位置与效果指标；新增版本有明确流程
  （追加 Version → 切 Active → 同步 `docs/prompt-audit.md`）。
- **代价**：改提示词必须改代码并走发布（不能用后台热改）。这是刻意的取舍：
  需要"快速试提示词"时，用评测脚本在本地对比候选版本，确认后再提交。
- **没做什么**：没有做多语言提示词、没有做 A/B 分流、没有做提示词自动优化。
  当前阶段的价值在"可审计"，不在"可实验"；实验能力等有真实对照需求再说。
