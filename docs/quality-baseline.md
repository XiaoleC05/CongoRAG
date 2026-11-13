# 质量基线（v6.3.0）

这份文档回答两个问题：**在本机能验证的范围内，"绿灯"具体长什么样；哪些结论必须等 CI。**

它记录的是 v6.3.0（R2 Prompt 审计与 Guardrails 注册表）的实测值：用例数、覆盖率、
前端产物、基准与 Playwright 冒烟。测法细节看 [performance.md](performance.md)，
测试约定（平台差异、崩溃探针、端到端验证）看 [testing.md](testing.md)。

> **采集点：commit `ea79901`（v6.3.0）+ 其上的 bundle 基线刷新 `94d8c6c`。**
> R2 的主要验收在 2026-11-14 03:37–03:39 冻结窗口内完成（384 个源文件指纹前后一致）；
> 因为冻结结束后同一工作区立即开始了 R3 改动，最终数字又在 **`ea79901` 的干净
> `git worktree`** 里复验了一次（不依赖当前工作区）。复验命令见 §11。

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

计时数字只代表本机；**用例数、skip 数、覆盖率、产物字节数与机器无关**，可跨机器比较；
`ns/op` 不可以（见 §8）。

## 2. 冻结与可审计性

R1 时踩过的教训：宣布"代码就绪"后仍有并发写入，一次 bench 撞上编辑到一半的
`arch_test.go` 直接报语法错误。所以每一组结论都必须能回答"它是哪份代码跑出来的"。

R2 的做法：

1. 对仓库内 384 个源文件（`.go/.ts/.tsx/.mjs/.yaml/.json/Makefile/.md/.png`，排除
   `node_modules`、`.git`、`.team`、`apps/api/web`、`data`、`pnpm-lock.yaml`）做 SHA256；
2. 每 10 秒重采样直到相邻两次一致（03:37:42 静止），跑完矩阵后再采样一次：
   **changed=0、added=0**；
3. 冻结结束后工作区立即出现 R3 改动，于是又用 `git worktree add --detach ea79901`
   在独立目录复验 Go 全量（不触碰共享工作区），本页 Go 数字取自这次复验。

复验片段：

```bash
# 不触碰当前工作区地复验某个 commit
git worktree add --detach /tmp/congorag-verify ea79901
cd /tmp/congorag-verify && go build ./... && go vet ./... && go test -count=1 ./...
git worktree remove --force /tmp/congorag-verify
```

## 3. 绿灯基线（本机可执行部分）

| 命令 | 实测 | 通过判据 |
| --- | --- | --- |
| `go build ./...` | 4.8s（冷 worktree；热缓存 1.9s） | exit 0、无输出 |
| `go vet ./...` | 1.0s（冷 worktree；热缓存 0.7s） | exit 0、无输出 |
| `go test -count=1 ./...`（含 `-json` 统计） | 5.3s | **pass 777 / fail 0 / skip 33**；无 FAIL |
| `go test -count=1 -cover ./...` | 6.7s | exit 0（覆盖率仅记录，见 §5） |
| `pnpm --filter web lint` | 0.5s（104 文件） | `0 warnings and 0 errors` |
| `pnpm --filter web exec tsc -b` | 4.0s | exit 0 |
| `pnpm --filter web test` | 6.9s（Vitest 内部 6.11s） | 29 文件 / 185 用例全过 |
| `pnpm --filter web build` | 4.8s（vite 段 476ms） | exit 0，产物落 `apps/api/web/` |
| `make generate-go` + `pnpm --filter api-client generate` | 0.2s / 1.0s | **幂等**：生成物哈希前后一致，`git diff` 为空 |
| `node scripts/bundle-report.mjs --check` | 0.2s | `结论：体积在预算内`，exit 0 |
| `make check-changelog` | 0.1s | `✓ CHANGELOG 格式没问题` |
| spectral 契约 lint（`npx @stoplight/spectral-cli@6.15.0 ... --fail-severity=warn`） | npx 缓存命中时可跑 | `No results with a severity of 'warn' or higher found!` |
| `go test ./internal/prompt`（10 条） | 0.6s | 全 PASS |
| Playwright `/prompts` 冒烟（route mock，非门禁） | 桌面/移动各 ~1.0s | 17 张卡片、0 console/pageerror、移动端横向溢出 0（见 §6） |

## 4. 用例口径

- `grep '^func Test'` = **589 个 Test 函数**；实际执行 **777 个通过用例**（含 222 个子测试）。
- 上一版（R1 冻结态）：576 个 Test 函数 / 764 个用例。R2 新增 13 个 Test 函数 / 13 个用例：
  `internal/prompt` 10 条（Validate / 目录排序与深拷贝 / SHA-256 确定性 / Render 变量与缺失变量 /
  可选变量 / 体例映射 / 历史版本单调 / grounding 契约）、`handler_prompt` 1 条、`call_options` 2 条。
- 33 条 skip 的构成（与 R1 相同，少一条都要解释）：
  - **32 条真库集成**：未设 `CONGORAG_TEST_DB_URL` 时 `t.Skip`（knowledge 7、agent 14、conversation 4、
    retrieval 3、llm 3、platform 1）；
  - **1 条平台差异**：`TestResolveMasterKey_GeneratedFilePermissionIs0600`（Windows 无 POSIX 权限位）。
- 22 个包：17 个 `ok`（其中 `internal` 只有 `arch_test.go`，`[no statements]`）+ 5 个无测试文件
  （`apps/api`、`apps/worker`、`apps/worker/internal/app`、`internal/domain`、`internal/testdb`）。
- Benchmark 函数 14 个（12 条生产基准 + 2 条对照，见 §8）。

## 5. 覆盖率（离线口径，`ea79901` 干净 worktree）

| 包 | 覆盖率 | 包 | 覆盖率 |
| --- | --- | --- | --- |
| grounding | 100.0% | llm | 39.2% |
| tokenize | 97.9% | knowledge | 44.0% |
| ctxmgr | 93.4% | conversation | 49.0% |
| pdfparse | 86.2% | agent | 51.9% |
| visualize | 84.4% | platform | 67.6% |
| **prompt（v6.3.0 新增）** | **78.6%** | retrieval | 70.7% |
| recommend | 75.9% | apps/api/internal/api | 29.8% |
| apps/api/internal/app | 10.8% | apps/worker（main/app） | 0.0% |
| internal/domain | 0.0% | — | — |

口径说明：

- 这是 `go test -cover ./...` 的**本地离线覆盖**：32 条真库集成测试没跑，`postgres.go` 这类文件
  天然偏低，**它不代表真实覆盖**；真实覆盖以 CI `integration` job 为准。
- 与 R1 相比的预期变化：api 28.1%→29.8%（新增 `handler_prompt_test`）、app 11.0%→10.8%
  （app.go 增加了 Validate 调用）、llm 37.2%→39.2%（call_options 测试）、新增 prompt 78.6%。
- **本轮不设覆盖率门禁**：离线覆盖率会把"只能 CI 验证"的路径误判为缺失。要做门禁从 R5 起，
  只对可离线测试的包设"不低于本表"的下限。

## 6. 前端

- **29 个测试文件 / 185 个用例全过**，Vitest 内部耗时 6.11s（environment 58%、import 26%、tests 10%）。
- `oxlint`：104 个文件、0 警告；`tsc -b` 4.0s。
- 新增「Prompt 审计」页（`/prompts`，懒加载）：5 条页面测试覆盖渲染字段（标题/key/生效版本/变量/
  代码位置/哈希前缀/采样参数）、版本切换（切换只换前端状态、不重新请求）、单版本不渲染切换器、
  空态、错误态（按 type 映射中文）；侧栏入口与路由一并测试。页面级行为测试覆盖 4/13 页。
- **Playwright 冒烟（不进仓库、不作门禁）**：用 `_record/node_modules/playwright` 起
  `vite --port 5174`，`context.route('**/api/v1/**')` 全量 mock，`/prompts` 的数据直接用
  overlay 注入的测试从**真实 handler 导出的 15,448 B JSON**（不是手写 fixture）：
  - 桌面 1440×900 / 移动 390×844：都渲染出 17 张卡片，哈希前缀、"注册表共 17 个条目"、
    版本切换按钮均可见；0 console/pageerror；移动端 `scrollWidth - clientWidth = 0`；
  - 首帧（dev 模式）约 1.0 秒（含 15KB JSON 与 17 张卡片）。
- `landing/` 仍为零测试（R4 处理）。

## 7. 构建体积

命令：`pnpm --filter web build` → `make bundle-report`（报告）/ `make bundle-check`（门禁）。
基线文件：`docs/perf/bundle-baseline.json`（已由 `94d8c6c` 刷新到 v6.3.0 构建，commit=ea79901）。

| 指标 | raw | gzip |
| --- | ---: | ---: |
| 首屏（入口 script + modulepreload + stylesheet） | **605,504 B** | **179,377 B** |
| 其中 JS / CSS | 512,539 / 92,965 B | 163,161 / 16,216 B |
| 入口 chunk `index-*.js` | 41,252 B | 11,963 B |
| 新增「Prompt 审计」chunk | 6,429 B | 2,501 B |
| 懒加载 `ReaderPage-*.js` | 444,098 B | 132,340 B |
| PDF worker（懒加载） | 1,178,002 B | 363,302 B |
| `assets/` 合计（60 个文件） | 2,682,880 B | 881,467 B |

- 相对 R1：首屏 gzip 179,100 → 179,377（**+0.15%**，预算 +4%）；`assets/` +7,579 B
  （其中 PromptsPage chunk 6,429 B）；新增 1 个懒加载业务 chunk，主逻辑无体积回退。
- 门禁判据：首屏 gzip +4%、单业务 chunk gzip +8%、ReaderPage/pdf worker +5%；基线里有、
  当前构建没有的业务 chunk 判红；新增业务 chunk raw >100KB 判红；文件名哈希按 6–12 位识别。
- 当前结论：`make bundle-check` → `体积在预算内`（刷新后的基线全部 +0.00%）。
- 可复现性：同一 commit 连续两次构建文件集与逐文件字节完全一致。

## 8. Benchmark 基线

R2 **没有重采样**微基准：本轮改动是提示词数据的搬迁与采样参数透传，§8 的 14 条基准路径
没有被改到。冻结态抽查（`make bench`，单次采样，机器有并发负载，只作路径健康检查）：
**14 条 benchmark 的 `allocs/op` 与 R1 表完全一致**（如 dedupe 48,664 B / 22 allocs、
FuseRRF 33,020 B / 134 allocs），`ns/op` 因负载噪声不作数据。下表沿用 R1 的 count=3 中位数：

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

去重优化对照（同进程、同语料，R1 冻结采样）：ns/op 217,375 → 160,187（−26.3%）、
B/op 191,001 → 48,664（−74.5%）、allocs/op 3,354 → 22（−99.3%）。

读法：`allocs/op` 与 `B/op` 跨机器稳定，`ns/op` 只在本机同一会话内可比；**benchmark 目前
没有自动门禁**（`make bench` 不进 CI），R5 再接。

## 9. Prompt 注册表与采样参数（v6.3.0 新增）

这一节记录 R2 的核心资产与**独立复核结果**（不是转述实现者的结论）：

- **规模**：17 条提示词 / 18 个版本（chat.system 有两版），覆盖对话、压缩、翻译、写作、
  对比、综述、图注、版本差异；全部生产字面量已改为 `prompt.Render`/`MustRender`。
- **SHA-256 一致性**：用 Node 的 `crypto` 对导出的 18 个版本内容独立重算 SHA-256，
  与 Go `prompt.ContentHash` **18/18 逐字节一致**；`/prompts` 响应中的哈希与内容同源
  （转换时现算、不落库，避免"改了文本忘了哈希"）。
- **结构校验 `Validate`**：用 `go test -overlay` 注入变异体（不改仓库），验证 8 类错误都能被抓到：
  Active 指错版本、模板占位符未声明、变量声明但模板未用、key 重名、缺代码位置、版本号不连续、
  缺变更原因、空 key。干净注册表通过。api 启动装配处调用它，失败拒绝启动。
- **`/prompts` 数据完整性**：overlay 注入的 handler 探针验证——17 条全量、chat.system 两个历史版本
  齐全、采样参数（translate 0.1 / writing 0.3）与指标（grounding.valid, n=2）正确透传、
  变量与代码位置存在、activeVersion 指向最后一版；每个版本哈希与 `prompt.Catalog()` 一致。
  另外核对 19 条 `codeRefs` 的 file 存在且 symbol 能在该文件里找到（19/19）。
- **采样参数透传（本轮唯一行为变更）**：适配器此前 `_ = opts` 丢弃 CallOption；现在
  temperature 走 Eino `WithExtraFields({"temperature": ...})`、maxTokens 走
  `WithMaxCompletionTokens`。已核对上游实现：ExtraFields 经 JSON Merge Patch **覆盖**
  请求体同名键（`libs/acl/openai` 的 `genRequest` + `internal/marshaller.go`），
  所以映射真实生效。受影响调用：`llm.Translate`（0.1）与 `llm.Generate`
  （写作/对比/综述/图注/版本差异，0.3）；普通聊天与 Agent 运行没有传 CallOption，行为不变。
- **契约**：新增 `/api/v1/prompts` 与 5 个 Prompt schema 后，本机 spectral lint（`--fail-severity=warn`）无告警；生成物重跑幂等（go/ts 两端哈希不变）。
- **已知边界**：`WithMaxCompletionTokens` 目前**没有生产调用点**（只有单测），所以"maxTokens
  透传"暂无运行时影响；将来若启用，注意部分 OpenAI 兼容服务只认 `max_tokens`。

## 10. 只能 CI / 带服务环境验证（本机结论一律标注为"待 CI"）

| 项 | 为什么本机不可执行 | 验证位置 |
| --- | --- | --- |
| `go test -race ./...` | CGO_ENABLED=0 且无 gcc | CI `go` job |
| 32 条真库集成测试 | 无 Docker / PostgreSQL | CI `integration` job |
| `scripts/contract-test.mjs`（prism proxy） | 需迁移好的 PG + 起 api + npx 联网 | CI `integration` job |
| `make test-integration` | testcontainers 依赖 Docker daemon | 带 Docker 的机器 / CI |
| docker job（三镜像构建 / compose config / migrate 真跑） | 无 Docker 守护进程 | CI `docker` job |
| spectral 契约 lint（含本轮新增 Prompt schema） | 本机已跑通（npx 缓存命中时）；无缓存时需联网 | CI `spec` job 为权威 |
| 真实 eval（`node evals/run_eval.mjs`） | 需 LLM Key + 模型 + 全栈 | 带服务环境；`evals/baseline.json` 仍是占位 |
| `make crash-probe` | 需 api + PG + chat 模型 | 带服务环境 |

**"本地全绿"不等于"集成全绿"**：本页只对 §3 的表负责，§10 的表在 CI 出结果之前保持未验证。

## 11. 如何重测

```bash
# 0) 先确认工作区静止（或直接复验某个 commit，见 §2 的 worktree 方法）
# 1) 本机绿灯
go build ./... && go vet ./... && go test -count=1 ./...
go test -count=1 -cover ./...
go test -count=1 ./internal/prompt ./internal/llm ./apps/api/internal/api   # 本轮新增/改动包
pnpm --filter web lint
pnpm --filter web exec tsc -b
pnpm --filter web test
# 2) 生成物幂等（契约改动后必须能重跑出同样内容）
make generate-go && pnpm --filter api-client generate && git diff --exit-code -- \
  apps/api/internal/api/generated.go packages/api-client/src/schema.d.ts
# 3) 构建体积
pnpm --filter web build && make bundle-report && make bundle-check
# 4) 微基准（中位数需要 -count=3）
go test -run '^$' -bench . -benchmem -count=3 ./internal/...
# 5) CHANGELOG 门禁 / Playwright 冒烟（R4 前用外部脚本）
make check-changelog
```

更新协议：

- 本页数字与"采集点"绑定；换机器/升级 Go/Vite 后重测并整体更新，不做局部替换。
- `docs/perf/bundle-baseline.json` 只在"体积变化是预期的"时刷新（`node scripts/bundle-report.mjs --json docs/perf/bundle-baseline.json`），
  且提交信息必须写明；门禁判红时先修代码。
- 新增 benchmark 时同步加进 §8 表；删除时在 PR 里说明原因。

## 12. R2 结论与已知缺口

R2 的本机可验证部分**通过**（§3 全绿、§7 门禁通过、§9 的独立复核全部成立）。已知缺口：

- `prompt.Validate` 在仓库里只有 happy-path 测试（`TestValidate` 只断言 NoError）；它的 8 条
  错误分支是本次用 overlay 变异体验证的，**建议补成仓库内的表驱动测试**，否则将来删掉某条
  检查不会有测试变红（R3/R5 顺手做）。
- 契约里 `PromptVersion.contentHash` 还没有 `pattern: ^[0-9a-f]{64}$` 约束，采样温度也没有
  取值范围；属于契约打磨，建议随 R3 契约加固一起做。
- benchmark 无自动门禁、bundle-check 未接进 CI、覆盖率无门禁、Playwright 仍是外部冒烟脚本
  —— 按计划由 R4/R5 接手。
- 32 条集成测试、`-race`、契约测试、镜像/迁移只能 CI —— 见 §10，R2 不对此下结论。
- `evals/baseline.json` 仍是占位（真实评测需要模型服务）—— 见 `evals/README.md`。
