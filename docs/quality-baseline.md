# 质量基线（v6.2.0）

这份文档回答两个问题：**在本机能验证的范围内，"绿灯"具体长什么样；哪些结论必须等 CI。**

它记录的是 2026-11-14 在 R1 工作区冻结后一次性采集的实测值，不是从别处抄来的目标值。
微基准的测法、一次真实优化（近重复去重）的前后对照在 [performance.md](performance.md)；
测试约定（平台差异、崩溃探针、端到端验证）在 [testing.md](testing.md)。

> 采集点：HEAD `dcc91f9` + R1 未提交工作区（v6.2.0）。
> 提交之后 HEAD 会变，本页的"commit"一栏需要随下一次重测更新；数字本身只与本页"环境"一节绑定。

## 1. 环境

| 项 | 值 |
| --- | --- |
| 机器 | Intel Core i7-14700KF（20 核 / 28 逻辑核），31.8 GB RAM |
| 系统 | Windows 11 家庭版（build 26300），PowerShell 7.7.0 |
| Go | go1.26.4 windows/amd64，**CGO_ENABLED=0**（`-race` 本机不可用） |
| Node / 包管理 | Node v22.19.0，pnpm 11.7.0 |
| 前端工具链 | Vite 8.3.0（rolldown）、Vitest 5.0.1、TypeScript ~6.0.2、oxlint 1.81.0 |
| 浏览器自动化 | Playwright 1.64.1（装在 `_record/node_modules`，仅冒烟，尚未工程化） |
| 不可用 | Docker daemon、PostgreSQL、任何 LLM API Key |

计时类数字只代表本机；**用例数、skip 数、覆盖率、产物字节数与机器无关**，可跨机器比较；`ns/op` 不可以（见 §8）。

## 2. 冻结与可审计性

为什么要有这一节：R1 验收期间发生过"宣布代码就绪后仍有并发写入"，一次 bench 直接撞上了
编辑到一半的 `internal/arch_test.go`（报 `string literal not terminated`）。**没有冻结与指纹的
"全绿"是不可审计的**——它只说明"某一次运行恰好通过了"，不说明"这份代码通过了"。

本页数字的采集方式：

1. 对仓库内 368 个源文件（`.go/.ts/.tsx/.mjs/.yaml/.json/Makefile/.md`，排除 `node_modules`、
   `.git`、`.team`、`apps/api/web`、`data`、`pnpm-lock.yaml`）做 SHA256 指纹；
2. 每 10 秒重采样，直到相邻两次完全一致（本次在 03:23:18 达到静止）；
3. 跑完整套矩阵后再采一次指纹：**changed=0、added=0** —— 下面的所有数字对应同一个冻结态。

复验片段（PowerShell，在任何一轮重测前先跑）：

```powershell
$exclude = 'node_modules|\.git\|apps\api\web|\.team|\data\'
$files = Get-ChildItem -Recurse -File -Include '*.go','*.ts','*.tsx','*.mjs','*.yaml','*.json','Makefile','*.md' |
  Where-Object { $_.FullName -notmatch $exclude -and $_.Name -ne 'pnpm-lock.yaml' }
$before = @{}; foreach ($f in $files) { $before[$f.FullName] = (Get-FileHash $f.FullName -Algorithm SHA256).Hash }
# ... 跑验收矩阵 ...
$after = @{}; foreach ($f in $files) { $after[$f.FullName] = (Get-FileHash $f.FullName -Algorithm SHA256).Hash }
($before.Keys | Where-Object { $after[$_] -ne $before[$_] }).Count   # 期望 0
```

## 3. 绿灯基线（本机可执行部分）

| 命令 | 实测 | 通过判据 |
| --- | --- | --- |
| `go build ./...` | 1.9s | exit 0、无输出 |
| `go vet ./...` | 0.7s | exit 0、无输出 |
| `go test -count=1 ./...`（含 `-json` 统计） | 4.3s | **pass 764 / fail 0 / skip 33**；无 FAIL |
| `go test -count=1 -cover ./...` | 4.5s | exit 0（覆盖率仅记录，见 §5） |
| `pnpm --filter web lint` | 0.5s（101 文件） | `0 warnings and 0 errors` |
| `pnpm --filter web exec tsc -b` | 3.8s | exit 0 |
| `pnpm --filter web test` | 6.3s（Vitest 内部 5.51s） | 28 文件 / 179 用例全过 |
| `pnpm --filter web build` | 4.6s（vite 段 524ms） | exit 0，产物落 `apps/api/web/` |
| `make bundle-check` | 0.3s | `结论：体积在预算内`，exit 0 |
| `go test -run '^$' -bench . -benchmem -count=3 ./internal/...` | 约 60s | exit 0，14 条 benchmark 全出数（见 §8） |
| Playwright 冒烟（route mock，非门禁） | 首帧 275ms | 0 console/pageerror（见 §6） |

合计约 90 秒可以重跑完本机部分（不含读库/起服务）。

## 4. 用例口径（为什么有两个数字）

- `grep '^func Test'` = **576 个 Test 函数**；实际执行用例 = **764 个通过**（含 222 个子测试）。
- dcc91f9 时的对照口径：**565 个 Test 函数 / 736 个通过用例**。R1 新增 11 个 Test 函数
  （`internal/arch_test.go` 5 条架构守卫 + `internal/retrieval/dedupe_spec_test.go` 6 条去重规格/
  等价测试），带来 28 个新用例。
- 33 条 skip 的构成（少一条或换一条都必须解释）：
  - **32 条真库集成**：未设 `CONGORAG_TEST_DB_URL` 时 `t.Skip`（knowledge 7、agent 14、conversation 4、
    retrieval 3、llm 3、platform 1）；
  - **1 条平台差异**：`TestResolveMasterKey_GeneratedFilePermissionIs0600`（Windows 无 POSIX 权限位）。
- 21 个包：16 个 `ok`（其中 `internal` 只有 `arch_test.go`，`[no statements]`）+ 5 个无测试文件
  （`apps/api`、`apps/worker`、`apps/worker/internal/app`、`internal/domain`、`internal/testdb`）。
- Benchmark 函数 14 个（12 条生产基准 + 2 条对照，见 §8）。

## 5. 覆盖率（离线口径）

| 包 | 覆盖率 | 包 | 覆盖率 |
| --- | --- | --- | --- |
| grounding | 100.0% | llm | 37.2% |
| tokenize | 97.9% | knowledge | 44.0% |
| ctxmgr | 93.4% | conversation | 49.0% |
| pdfparse | 86.2% | agent | 51.9% |
| visualize | 84.4% | platform | 67.6% |
| recommend | 75.9% | retrieval | 70.7% |
| apps/api/internal/api | 28.1% | apps/api/internal/app | 11.0% |
| apps/worker（main/app） | 0.0% | internal/domain | 0.0% |

口径说明：

- 这是 `go test -cover ./...` 的**本地离线覆盖**：32 条真库集成测试没跑，`postgres.go` 这类文件
  天然偏低，**它不代表真实覆盖**；真实覆盖以 CI `integration` job 为准。
- 与 dcc91f9 相比的变化都是预期的：`knowledge` 45.5%→44.0%、`tokenize` 97.6%→97.9%
  （切词实现从 knowledge 搬到 tokenize，knowlege 里那份被删掉）、`retrieval` 68.2%→70.7%（新增测试）。
- **本轮不设覆盖率门禁**：离线覆盖率会把"只能 CI 验证"的路径误判为缺失。要做门禁从 R5 起，
  只对可离线测试的包设"不低于本表"的下限。

## 6. 前端

- **28 个测试文件 / 179 个用例全过**，Vitest 内部耗时 5.51s（environment 58%、import 27%、tests 9%）。
  早先一次测到 27s 是因为与 Go 测试并行跑——本页采用干净环境下的数字，重测时别在跑 bench 的同时跑它。
- `oxlint`：101 个文件、0 警告（24ms）；`tsc -b` 3.8s。
- 页面级行为测试覆盖 3/12 页（AgentDetail / Conversation / KnowledgeBaseDetail）；R1 新增的是
  `lib/apiResult` 的错误传递链测试，没有新增页面测试。`landing/` 仍为零测试。
- **Playwright 冒烟（不进仓库、不作门禁）**：用 `_record/node_modules/playwright` 起
  `vite --port 5174`，`context.route('**/api/v1/**')` 全量 mock，覆盖知识库页（桌面 1440×900 /
  移动 390×844）与 onboarding 守卫重定向，0 console/pageerror、首帧 275ms。R4/R5 工程化后
  它会变成截图矩阵与 E2E 门禁（见 §11）。

## 7. 构建体积

命令：`pnpm --filter web build` → `make bundle-report`（报告）/ `make bundle-check`（门禁）。
基线文件：`docs/perf/bundle-baseline.json`。

| 指标 | raw | gzip |
| --- | ---: | ---: |
| 首屏（入口 script + modulepreload + stylesheet） | **604,457 B** | **179,100 B** |
| 其中 JS / CSS | 511,918 / 92,539 B | 162,959 / 16,141 B |
| 入口 chunk `index-*.js` | 40,962 B | 11,864 B |
| 懒加载 `ReaderPage-*.js` | 444,056 B | 132,324 B |
| PDF worker（懒加载） | 1,178,002 B | 363,302 B |
| `assets/` 合计（58 个文件） | 2,675,301 B | 878,558 B |

- 门禁判据：首屏 gzip +4%、单个业务 chunk gzip +8%、ReaderPage/pdf worker +5%；**基线里有的
  业务 chunk 在当前构建里消失、或新增业务 chunk raw >100KB，都判红**（必须显式刷新基线，
  让变化出现在 diff 里）；文件名哈希按 6–12 位识别，避免工具升级后门禁静默失效。
- 当前结论：`make bundle-check` → `体积在预算内`（所有项 +0.00%）。
- 可复现性：同一 commit 连续两次构建，文件集与逐文件字节完全一致；体积门禁因此可以按精确值比。
- 注意：`docs/perf/bundle-baseline.json` 的 `commit` 字段是"生成基线时的 HEAD"。R1 提交后需要
  重跑一次 `node scripts/bundle-report.mjs --json docs/perf/bundle-baseline.json` 刷新它（只更新
  commit/时间戳，数字应保持不变）。

## 8. Benchmark 基线

测法：`go test -run '^$' -bench . -benchmem -count=3 ./internal/...`（每条约 3 次采样取中位数，
2026-11-14 冻结态；GOMAXPROCS=28）。`make bench` 是单次采样的快速版，`make bench-dedupe` 只跑去重相关三条。

| 基准 | ns/op（中位） | B/op | allocs/op | 覆盖的路径 |
| --- | ---: | ---: | ---: | --- |
| tokenize/Text_Paragraph | 21,756 | 40,624 | 466 | worker 每分块切词（~1KB 中英混排） |
| tokenize/Text_Query | 1,272 | 2,016 | 31 | 用户提问切词 |
| tokenize/Tokens_Paragraph | 17,259 | 26,000 | 456 | SimHash 用的不去重切分 |
| tokenize/Query | 1,888 | 2,432 | 33 | 拼 `to_tsquery` |
| grounding/Verify | 9,116 | 9,797 | 152 | 回答结束时的引用落地检查 |
| retrieval/DedupeNearDuplicates | 160,187 | 48,664 | 22 | 近重复去重（topK=5，50 候选） |
| retrieval/DedupeNearDuplicates_Top20 | 1,023,263 | 218,672 | 92 | 去重后凑 20 条（对比/写作） |
| retrieval/FuseRRF | 19,035 | 33,020 | 134 | 向量 + 词法 RRF 融合（各 50 条） |
| knowledge/ContentFingerprint | 305,413 | 203,266 | 3,376 | 上传去重的 SimHash（~20KB 正文） |
| knowledge/ParseAndChunk | 177,172 | 264,808 | 296 | 解析产物切分（~48KB 文本） |
| knowledge/BuildCitationGraph | 1,965 | 4,072 | 14 | 20 篇论文构图 + PageRank |
| platform/CursorRoundTrip | 226 | 512 | 7 | keyset 游标编解码 |
| visualize/Build | 4,282,002 | 14,632,432 | 29,232 | CSV（500 行）→ 图表代码 + Caption |
| **对照** retrieval/DedupeNearDuplicates_LegacyReference | 217,375 | 191,001 | 3,354 | v6.1.0 字符串 shingle 实现（仅测试内） |

近重复去重同进程对照（同一份语料、同一次运行）：

| 指标 | v6.1.0 字符串实现 | 冻结态哈希实现 | 变化 |
| --- | ---: | ---: | ---: |
| ns/op | 217,375 | 160,187 | −26.3% |
| B/op | 191,001 | 48,664 | −74.5% |
| allocs/op | 3,354 | 22 | −99.3% |

读法：

- **`allocs/op` 与 `B/op` 跨机器稳定**（由代码分配决定），`ns/op` 只在本机同一会话内可比；
  换机器或 Go 版本时要重跑本表，不要拿旧 `ns` 判回归。
- 本条去重的等价性有独立 oracle：`dedupe_spec_test.go` 保留旧实现作参照，覆盖 shingle 集合、
  Jaccard、整函数决策（含阈值两侧 55%–98% 的 26 组语料）三类断言。
- **benchmark 目前没有自动门禁**（`make bench` 不进 CI、allocs 也没有判据）。R5 再接回归门禁；
  在那之前它是"记录基线 + 人看趋势"，不是红线。

## 9. 只能 CI / 带服务环境验证（本机结论一律标注为"待 CI"）

| 项 | 为什么本机不可执行 | 验证位置 |
| --- | --- | --- |
| `go test -race ./...` | CGO_ENABLED=0 且无 gcc | CI `go` job |
| 32 条真库集成测试 | 无 Docker / PostgreSQL | CI `integration` job |
| `scripts/contract-test.mjs`（prism proxy） | 需迁移好的 PG + 起 api + npx 联网 | CI `integration` job |
| `make test-integration` | testcontainers 依赖 Docker daemon | 带 Docker 的机器 / CI |
| docker job（三镜像构建 / compose config / migrate 真跑） | 无 Docker 守护进程 | CI `docker` job |
| spectral 契约 lint | npx 联网 + 版本锁定 | CI `spec` job |
| 真实 eval（`node evals/run_eval.mjs`） | 需 LLM Key + 模型 + 全栈 | 带服务环境；`evals/baseline.json` 仍是占位 |
| `make crash-probe` | 需 api + PG + chat 模型 | 带服务环境 |

**"本地全绿"不等于"集成全绿"**：本页只对 §3 的表负责，§9 的表在 CI 出结果之前保持未验证。

## 10. 如何重测

```bash
# 0) 先确认工作区静止（见 §2 的指纹片段），再开始
# 1) 本机绿灯
go build ./... && go vet ./... && go test -count=1 ./...
go test -count=1 -cover ./...
pnpm --filter web lint
pnpm --filter web exec tsc -b
pnpm --filter web test
# 2) 构建体积
pnpm --filter web build
make bundle-report
make bundle-check
# 3) 微基准（中位数需要 -count=3）
go test -run '^$' -bench . -benchmem -count=3 ./internal/...
# 4) Playwright 冒烟（R4 前用外部脚本；仓库内工程化在 R5）
node <外部脚本>   # _record/r1-smoke.cjs：vite --port 5174 + page.route mock
```

更新协议：

- 本页数字与"环境"一节绑定；换机器/升级 Go/升级 Vite 后重测并整体更新，不做局部替换。
- `docs/perf/bundle-baseline.json` 只在"体积变化是预期的"时用 `--json` 刷新，且 diff 必须能 review；
  `make bundle-check` 判红时先修代码，不刷新基线。
- 新增 benchmark 时同步加进 §8 表；删除时在 PR 里说明原因。

## 11. R1 结论与已知缺口

R1 的本机可验证部分**通过**（§3 全绿、§7 门禁通过、§8 基线可复现）。已知缺口都留给后续轮次：

- benchmark 无自动回归门禁、bundle-check 未接进 CI —— R5；
- Playwright 仍是外部冒烟脚本，未进仓库、无截图矩阵 —— R4/R5；
- 覆盖率无门禁 —— R5；
- 32 条集成测试、`-race`、契约测试、镜像/迁移只能 CI —— 见 §9，R1 不对此下结论；
- `evals/baseline.json` 仍是占位（真实评测需要模型服务）—— 见 `evals/README.md`。
