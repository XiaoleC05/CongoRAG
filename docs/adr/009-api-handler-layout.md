# ADR-009：HTTP handler 按资源域分文件，而不是一个大 server.go

- 状态：已采纳
- 日期：2026-11-14
- 相关：`apps/api/internal/api/server.go`、`apps/api/internal/api/convert.go`、
  `apps/api/internal/api/handler_*.go`

## 背景

`apps/api/internal/api/server.go` 在 v6.1.0 时有 2049 行，混装了六个资源域的
端点（知识库/文档、科研助手、arXiv、BYOK 模型服务、会话与 SSE、Agent）、
20 个契约 DTO 转换函数，以及若干公共请求工具。

包边界本身是对的：`internal/` 下的业务包不 import gin、不知道 HTTP 状态码，
路由由 `generated.go` 按契约挂载。问题在**文件粒度**：改一个知识库端点要在同一个
文件里翻几百行，一次 review 的 diff 会横跨多个无关资源域，新增端点时也很难
判断"该放在哪、有没有重复的转换函数"。

拆分前先确认了它不是别的问题：最长函数不到 80 行、注释占比 19.2%，
所以"函数太长"不成立；真正成立的是"一个文件承担了九个关注点"。

## 决策

**同一个 `api` 包内按资源域分文件：**

| 文件 | 职责 |
| --- | --- |
| `server.go` | `Deps` / `Server` / `NewServer`、健康检查、公共请求工具 |
| `convert.go` | 契约类型 ↔ 业务类型的纯函数转换 |
| `handler_knowledge.go` | 知识库与文档（CRUD、上传下载、版本差异、重索引、引用图） |
| `handler_research.go` | 检索调试、翻译、CSV 可视化、写作、对比、综述 |
| `handler_arxiv.go` | arXiv 推荐、反馈、一键入库 |
| `handler_settings.go` | provider/model（BYOK）与用量 |
| `handler_conversation.go` | 会话、消息、SSE 与幂等补发 |
| `handler_agent.go` | Agent 配置、运行、取消、恢复、步骤与事件流 |

**不拆成子包。** 所有 handler 共享同一个 `Server` 与 `Deps`，拆包要么把依赖
重新声明一遍（`Deps` 变成公共大对象在各子包间传递），要么引入一层转发——
两个方案都比"同包不同文件"多出真实成本，而文件粒度已经解决了 review 与
定位问题。**也不合并同域文件**：资源域就是天然的修改边界。

## 后果

- **好处**：改一个资源域只碰一个文件；import 只保留该文件真正用到的包
  （拆分前是同一份约 28 条的全量清单，拆分后每文件 6–16 条）；
  新增端点时"放哪"有明确答案。
- **约束**：`var _ ServerInterface = (*Server)(nil)` 这条编译期断言保留在
  `server.go`——契约里加了端点而实现没跟上时，编译仍然失败在同一处。
- **行为必须零变化**：这次拆分是纯搬运。验收方式是拆分前后
  `go doc -all ./apps/api/internal/api` 的 diff 只允许出现新增的文件头注释；
  端点测试一行不改、全部通过；去掉注释后新旧代码行的多重集为 0 差异。
  拆分时失去的只有 **22 行旧的"章节分隔注释"**（14 条分隔线 + 8 个章节标题，
  如 `// 会话`、`// 文档`）——文件边界取代了它们的作用；同时新增 44 行
  文件头说明（每个文件"为什么在这里"）。
- **如果将来再膨胀**：单个 `handler_*.go` 超过约 800 行时，先看是不是混进了
  第二个资源域，而不是继续往同一个文件里加。
