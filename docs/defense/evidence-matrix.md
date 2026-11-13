# 评分证据矩阵

> 把评分表每一细项对到仓库里可点开、可运行的证据。评委/答辩时按这张表找，
> 不需要在视频里猜哪一段对应哪个分。
> 分值来自赛事赛道详情页（35% Agent/大模型技术深度、25% 科研创新、10% 工程质量、
> 10% 业务闭环、10% UI/UX、10% 学术价值）。

| 维度 | 细项 | 分值 | 证据（代码 / 测试 / 文档 / 截图 / 提交） |
| --- | --- | ---: | --- |
| a | Agent / 工作流编排 | 9 | `internal/agent/`（工具循环、checkpoint、取消/恢复）；运行轨迹页 `RunTracePage` + `docs/assets/agent-run.png`；`docs/adr/006`、`docs/adr/007`；`internal/agent/*_test.go`（含崩溃恢复） |
| a | 抗幻觉 RAG 与溯源 | 8 | 混合检索 `internal/retrieval/`（bigram FTS + pgvector + RRF）；`internal/grounding/` 句子级引用落地；citation 随 SSE 下发；固定拒答；`docs/evaluation/` 评测脚本与结果 |
| a | 多模态与细粒度解析 | 7 | `sidecars/pdf-service/`（PyMuPDF + OCR）、`internal/pdfparse/`；页/坐标/章节路径；541 页与扫描件用例见 `docs/submission/评测报告.md` |
| a | Function Calling 与工具 | 6 | 工具注册表 `internal/agent/tool_*.go` + `GET /api/v1/tools`；arXiv 工具与一次真实运行轨迹（Agent 页） |
| a | Prompt 与 Guardrails | 5 | `internal/prompt/` 注册表（版本/SHA-256/采样/代码位置）、`docs/prompt-audit.md`、`docs/adr/010`、审计页 `docs/assets/prompts-audit.png`；启动校验；采样参数透传修复 |
| b | 图谱挖掘创新 | 10 | `internal/knowledge/graph.go`（引用图 + PageRank 基石）、`POST /citation-survey`（Future Work 只留开放方向）、图谱面板截图 |
| b | RAG 方案创新 | 8 | 上传语义去重（SimHash）+ 版本差异；v6.2.0 去重性能改造（`docs/performance.md`，−24.1% 时间 / −99.3% allocs）；等价性测试 `dedupe_spec_test.go` |
| b | 科研交互模式创新 | 7 | 阅读器双语滚动同步/划词/悬停翻译；[n] ↔ References 双向跳转；bbox 锚点跳转；命令面板；截图 `docs/assets/reader-*.png`、`command-palette.png` |
| c | 代码与架构 | 4 | 模块化单体 + 位点端口（`docs/adr/001`）；HTTP 层按资源域拆分（`docs/adr/009`）；`internal/arch_test.go` 依赖方向守卫；Go 800+ / 前端 200+ 用例 |
| c | 部署可复现 | 3 | `deployments/`（docker/startup/production）、`.env.example`、`docs/deployment.md`（runbook）、`make check` 一键自查 |
| c | 性能与内存 | 3 | `docs/performance.md`（14 条 benchmark + 优化对照）、`make bench-gate` 回归门禁、`make bundle-check` 体积预算、`docs/loadtest-report.md`（SSE 并发压测） |
| d | 痛点契合 | 5 | `docs/modules.md` 六模块；README 场景叙事；landing 首页分节 |
| d | 官方用例通过率 | 5 | 去重/扫描件/541 页长书/问答四类：`docs/submission/评测报告.md`、集成测试（CI integration job）、`docs/quality-baseline.md` |
| e | 学术界面与图谱美观 | 4 | 深色学术风 landing + 应用壳层；`docs/assets/*.png`（真实界面截图）；响应式 390px 单列 |
| e | 联动阅读 | 3 | `web/src/pages/ReaderPage.tsx` + `web/src/lib/reader.ts`；窄屏目录/翻译抽屉（`docs/assets/reader-mobile-outline.png`）；bbox 随页失效 |
| e | 流式与错误边界 | 3 | `docs/sse-protocol.md`（帧格式/续传/心跳）、`web/src/lib/streamChat.ts`、`RouteErrorBoundary`、RFC7807 错误映射（`web/src/lib/errors.ts`） |
| f | AI4S 价值 | 5 | README 的科研工作流叙事；六模块闭环；评测数据（hit@1/hit@3/MRR） |
| f | 开源潜力 | 5 | MIT；README 架构图 + Roadmap + 扩展点；CI 八 job（含 E2E/契约/集成）；CHANGELOG 可追溯；`docs/architecture.md` |

## 一句话复现路径

```bash
make up && make migrate-up && make river-migrate-up   # 数据库
make build && make dev                                 # api（另开窗口 make dev-worker）
make check                                             # Go + 前端单测 + lint
make bench-gate && make bundle-check                   # 性能/体积门禁
make test-e2e                                          # Playwright（mock API，不需要数据库）
```

> 记不清某个数字时看 `docs/quality-baseline.md`：它是冻结态一次性采集的实测值，
> 与本文档的定性结论互相印证。
