# 采用的工程与写作 Skill

这一页记录项目实际采用的 AI 辅助 skill 与规则，以及它们在仓库里对应的改动。
不是工具清单，换一个模型或换一个 IDE 也能照着做。

## 1. Humanizer：把 AI 腔从文档里拿掉

来源：本地 `humanizer` skill（文件系统 skill），核心检查项是 staging、机械三段式、
无依据的强调、格式套路、聊天残留和"给错读者"。

落地：

- README 开头从"能力清单 + 基线叙事"改成"上传论文→问问题→点引用回到原文"的具体动作；
- 演示站首页的 hero 和四张卡片改成读者能直接做的事，不再写"可溯源、抗幻觉"这类标签；
- 提交材料里的"作品在 v5.0.2 基础上补齐"改为单一主线描述。

## 2. Code Review：按仓库标准 + Fowler smell 基线看改动

来源：本地 `code-review` skill。清理改动没有新增业务逻辑，重点看路径、引用与
"改一半"的旧名字。

落地：

- 全仓替换 `docs/competition/*`、`deployments/competition`、`cup-2026` 等旧路径/旧分支名；
- 删除 `site/`、`ops/`、`site-release.yml` 与旧视频脚本，避免两套文档互相矛盾；
- 保留 ADR 与历史 tag，只在 CHANGELOG 里记录版本演进，不再把"原项目/参赛项目"并列；
- 重新生成 OpenAPI 代码与前端 schema，`go build`、`go test`、`vitest` 全部通过。

## 3. Agentic Architecture Review：不为架构图引入复杂度

来源：开源 skill `allierays/agentic-architecture-review` 的核心原则：先给组件分类，
不要让 Agent 做问题不需要的事；自主性上限由验证能力决定；复杂度要靠错误分析挣来。

落地：

- 明确 Agent 只做多步工具调用（检索/计算），写操作必须用户触发；
- 单 Agent + 工具循环，不引入多 Agent 路由；
- 工具调用落库、可取消/恢复，回答可信度由 evidence + grounding 决定，不由 Agent 自信决定；
- 这些边界写进 [docs/modules.md](modules.md) 第 8 节。

## 4. Frontend Design + Vercel React Best Practices

来源：本地 `frontend-design` 与 `vercel-react-best-practices` skill。

落地：

- 首页只保留一个明确动作（进演示），减少模板化的 hero + 统计数字组合；
- 批量上传按顺序提交并显示进度，不并发打满上传接口；
- arXiv 自动追踪的 interval 在组件卸载时清理，不留下后台轮询；
- 前端继续用 Vite 分包，不引入额外图表/动画库。

## 5. 没采用的

- 多 Agent 编排、复杂图数据库、前端图表库：当前问题不需要，增加部署与调试成本；
- AI 生成的"未来工作"式空话：文档只写已经跑过的数据和明确限制。


## 6. Design Taste：把网站从模板感里拉出来

来源：本地 `design-taste-frontend`（tasteskill v2）。它先要求从主题本身找构图，而不是套 hero + 三张卡片；
再用真实浏览器检查标题、字号、对比度、空屏和移动端。

落地：

- 首页改为“论文页 + 页边批注”的视觉：纸张底色、衬线标题、编号小节、引用上标、真实界面截图做 Figure；
- 去掉玻璃拟态、渐变 hero、统计数字和三张功能卡；首屏只保留一个动作（打开演示）；
- 六张真实界面截图只作为产品证据，不画假插画；手机端每个小节先读标题和结论，再看图；
- 正文最小 13px，引用校验说明的对比度从 4.1:1 提到 4.5:1 以上；补 focus-visible、skip link、alt 和 canonical/OG/JSON-LD；
- 性能：首页无外部字体和 JS 依赖；首图 eager，其余 lazy + 宽高；静态资源走 `/landing/` 并缓存 7 天。

品牌：ConGoRAG → CongoRAG，导航、页面标题、favicon、README、技术文档与提交材料统一。

验证：Playwright 在 1440 / 390 两个宽度检查图片加载、横向溢出、console error；前端 171 个测试通过。

## 6. 没采用的

- 多 Agent 编排、复杂图数据库、前端图表库：当前问题不需要，增加部署与调试成本；
- AI 生成的“未来工作”式空话：文档只写已经跑过的数据和明确限制。
