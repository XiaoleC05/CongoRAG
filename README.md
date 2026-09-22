# ConGoRAG

[![CI](https://github.com/XiaoleC05/CongoRAG/actions/workflows/ci.yml/badge.svg)](https://github.com/XiaoleC05/CongoRAG/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Version](https://img.shields.io/badge/version-3.0-blue)](CHANGELOG.md)

本地优先的 RAG 问答与 Agent 平台。文档、向量、API Key 全部留在你自己的机器上，
模型服务由你提供（BYOK）。

api 进程默认只监听 `127.0.0.1:3210`，不对局域网开放。API Key 用 AES-GCM 加密后
存进本地数据库，主密钥在首次启动时生成并落盘。

当前**已发布**的版本是 **v3.0**（2026-09-21）。`main` 上已经积累了下一版的
改动——下文的能力与端点因此可能领先于你能下载到的最新 tag，逐条列在
[CHANGELOG.md](CHANGELOG.md) 的 `Unreleased` 一节里。

<!--
TODO(截图)：README 不虚构界面图。补图时把截图放进 docs/assets/，然后在下面插一行：
![对话页](docs/assets/conversation.png)
-->

## 目录

- [数据流](#数据流)
- [核心能力](#核心能力)
- [快速开始](#快速开始)
- [使用流程](#使用流程)
- [技术栈](#技术栈)
- [架构](#架构)
- [配置](#配置)
- [API](#api)
- [开发](#开发)
- [测试](#测试)
- [贡献](#贡献)
- [目录结构](#目录结构)
- [License](#license)

## 数据流

两条链路共用同一个数据库：写入侧是异步的（上传即返回，worker 慢慢处理），
读取侧是同步的（提问当场检索、组装上下文、边生成边下发）。

```mermaid
flowchart TB
  D[文档] -->|上传入队| W["worker：解析 → 切分 → 向量化"]
  W --> V[("pgvector<br/>halfvec 列 + HNSW 索引")]
  U[提问] --> R[检索：按知识库限定范围]
  V --> R
  R --> C["上下文组装：按 token 预算<br/>超预算交给模型压缩"]
  C --> A[Agent 运行时]
  A --> S[SSE 流式回答 + citation]
  G["工具：知识库检索 / 会话检索 / 计算器"] --- A
```

## 核心能力

- **知识库管理**：新建、改名、删除知识库，列表与详情页
- **文档上传与处理**：multipart 上传 → 落盘 → 入队 → 解析 / 切分 / 向量化，状态可查
- **向量检索**：pgvector 的 HNSW 索引（BYOK 引导时按 embedding 维度建），按知识库
  范围限定，返回片段、相似度分数与文件名
- **流式问答**：发消息的响应本身就是 SSE 流，逐 token 下发，citation 随流推送；
  断了带 `after_event_id` 把之后的事件补回来（客户端用 `fetch` + `ReadableStream`
  解析，不用 `EventSource`，见 [ADR-005](docs/adr/005-sse-over-fetch.md)）
- **上下文管理**：按 token 预算组装上下文，超预算的历史交给模型压缩
- **Agent**：工具注册表（计算器、知识库检索、会话检索），每次执行是一条 run，
  每一步的轨迹单独落库；运行可以**取消**（终态是 `cancelled` 而不是 `failed`），
  也可以从断点**恢复**（进程被杀之后跳过已完成的步骤，按工具的副作用等级决定
  能不能重放）
- **幂等重放**：`POST /conversations/{id}/messages` 与 `POST /agents/{id}/runs` 都接受
  `Idempotency-Key`，同键重发不会重新执行，而是把那一轮/那条 run 已经记录的事件
  按原来的 `event_id` 补发一遍
- **检索调试**：`POST /knowledge-bases/{id}/search` 直接返回命中的分块与相似度——
  "为什么这个问题答错了"是没检索到、还是排序不对，在这里能当场看出来
- **重新索引**：单份文档或整个知识库都能重跑（换模型之后、或某一份处理失败时）
- **BYOK**：provider / model 配置存数据库，API Key 用 AES-GCM 加密后落盘；换
  embedding 模型时可确认「清空并重建」，不必手工清库
- **用量计量**：每次模型调用记一行 token 用量，`GET /api/v1/usage` 按模型聚合
- **优雅退出**：Ctrl-C 会先拒新请求、排空在途 SSE、给 worker 的在途任务留收尾时间
- **单体交付**：前端产物 `go:embed` 进 Go 二进制，一个进程同时提供 API 和界面

## 快速开始

### 前置条件

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.26.4（见 `go.mod`） | 编译 api 和 worker |
| Docker | 任意近期版本 | 只用来跑 PostgreSQL，见 `deployments/docker/docker-compose.yml` |
| Node.js + pnpm | 22 / 11 | 构建前端；只在改前端时需要 |

命令行工具装进 `GOPATH/bin`，`Makefile` 会自己把这个目录拼进 `PATH`：

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1
go install github.com/riverqueue/river/cmd/river@v0.47.0
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
```

数据库镜像固定为 `pgvector/pgvector:0.8.6-pg18`，已经包含 `vector` 扩展——
`migrations/0001_init.up.sql` 的第一句就是 `CREATE EXTENSION IF NOT EXISTS vector`，
用官方 `postgres` 镜像会在迁移这一步失败。

### 三步跑起来

```bash
# 1. 起 PostgreSQL（Docker Desktop 里能看到 congorag-postgres 容器）
make up

# 2. 建表。两套迁移系统都要跑：业务表归 golang-migrate，River 的队列表归 river
make migrate-up
make river-migrate-up

# 3. 构建并启动：构建前端产物 → go:embed 进二进制 → 起服务
make build
make dev
```

浏览器打开 <http://127.0.0.1:3210>，首次打开会跳到引导页。

处理文档上传还需要第三个终端跑 worker，否则文档会一直停在待处理状态：

```bash
make dev-worker
```

## 使用流程

### 1. 引导页：接入模型服务

第一次打开要求填模型服务的 `base_url`、`api_key` 和模型名，以及这个模型是否支持
工具调用（默认勾选——取消勾选后，带工具的 Agent 会立刻不能创建和运行）。API Key
加密后落库，之后不用再填。

embedding 模型的维度决定向量列的形状：HNSW 索引在 BYOK 引导时按维度建。

### 2. 知识库页：建库并上传文档

新建知识库后进详情页上传文档。上传是 multipart，服务端先落盘再入队，请求立刻
返回；解析、切分、向量化由 worker 异步做，页面上的文档状态跟着变。单份文档的
处理上限是 30 分钟，worker 同时处理 10 个任务。

一份文档处理失败、或者你换过 embedding 模型之后，可以对单份文档或整个知识库
重新索引。

### 3. 对话页：提问并拿到带 citation 的回答

发一条消息，回答通过 SSE 逐 token 下发，citation 随流推送，点开能看到命中的
片段来自哪份文件。这条流断掉之后，带 `after_event_id` 请求
`GET /conversations/{id}/events` 会把之后的事件补发一遍——补完就结束响应，它不
持有连接等新事件。同一条消息带同一个 `Idempotency-Key` 重发时，服务端不重新
生成，而是把那一轮的事件按原来的 `event_id` 补发一遍。

### 4. Agent 页：让模型带工具跑多步任务

新建 Agent 时指定用哪个模型、开哪些工具（工具目录来自 `GET /api/v1/tools`，内置
三个：知识库检索、会话检索、计算器）。每次执行是一条 run，运行轨迹——每一步调了
哪个工具、传了什么参数、拿到什么结果——逐步落库，在运行详情页可查。

## 技术栈

| 层 | 选型 |
| --- | --- |
| 后端 | Go 1.26、Gin v1.12 |
| 数据库 | PostgreSQL 18 + pgvector 0.8.6（`halfvec` 列 + HNSW 索引，见 [ADR-004](docs/adr/004-halfvec-embeddings.md)） |
| 异步队列 | River v0.47，与业务表同库（见 [ADR-003](docs/adr/003-two-migration-systems.md)） |
| Agent 运行时 | CloudWeGo Eino v0.9 |
| 契约 | OpenAPI 3.0.4，唯一真相；Go 侧 oapi-codegen，TS 侧 openapi-typescript |
| 前端 | React 19、TypeScript 6.0、Vite 8 |
| 前端数据层 | TanStack Query v5、React Router 8 |
| UI | Tailwind CSS 4、Radix UI、shadcn、lucide-react |
| 测试 | Go test + testify；Vitest + Testing Library；oxlint |
| 交付 | 前端产物 `go:embed` 进单个 Go 二进制，两个进程（api / worker） |

## 架构

模块化单体：一个 api 进程 + 一个 worker 进程，模块边界用 Go 包表达，不拆微服务。
PostgreSQL 是唯一的数据源——业务数据、向量（pgvector）、异步队列（River 用同一
个库做队列表）都在里面，所以"写业务记录"和"入队"可以在一个事务里完成。

```mermaid
flowchart LR
  B[浏览器 SPA] -->|REST / SSE| API[apps/api]
  API --> PG[(PostgreSQL 18 + pgvector)]
  W[apps/worker] --> PG
  API -.入队.-> PG
  PG -.消费.-> W
  API --> LLM[模型服务<br/>由用户配置]
  W --> LLM
```

`internal/` 下的八个业务包：`domain`、`platform`、`llm`、`knowledge`、`retrieval`、
`ctxmgr`、`conversation`、`agent`。两个进程各自的装配根在
`apps/*/internal/app/app.go`。依赖方向有几条不靠人盯、靠编译期保证的约定，改动时
不要绕过：

- 业务包（`internal/` 下那八个）不 import gin、不知道 HTTP 状态码
- 跨包的「我需要什么」由**消费方**声明成 port，实现方零 import 边
  （例子：`llm.DocumentReindexer` 由 llm 声明、`knowledge.Usecase` 实现，连接点只在
  `apps/api/internal/app/app.go` 的装配顺序上）
- 两个进程各有自己的装配根，重复十几行是正常的，不要为此造共享装配包

设计取舍写在 [`docs/adr/`](docs/adr/) 里（模块化单体、契约版本、两套迁移系统、
为什么用 halfvec、为什么客户端不用 EventSource、checkpoint 分层、恢复语义、
幂等重放窗口）。流式协议的字节格式规范在 [`docs/sse-protocol.md`](docs/sse-protocol.md)，
平台相关行为怎么测在 [`docs/testing.md`](docs/testing.md)，发布流程在
[`docs/releasing.md`](docs/releasing.md)，用户视角的升级与回滚在
[`docs/upgrading.md`](docs/upgrading.md)，契约 lint 的规则与豁免理由在
[`docs/contract-lint.md`](docs/contract-lint.md)。

不想从源码构建的话，Release 里附带一个**启动包**（compose + `.env.example` +
镜像 tarball），解压起来就能用，见 [`deployments/startup/`](deployments/startup/)。

## 配置

全部通过环境变量配置，没有配置文件。只有 `CONGORAG_DB_URL` 是必填的。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CONGORAG_DB_URL` | 无（必填） | PostgreSQL 连接串 |
| `CONGORAG_LISTEN_ADDR` | `127.0.0.1:3210` | api 监听地址。默认只绑回环 |
| `CONGORAG_PORT` | `3210` | 对外端口，只用于日志提示 |
| `CONGORAG_DOCUMENTS_DIR` | `./data/documents` | 上传文件的存储根目录 |
| `CONGORAG_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `CONGORAG_MASTER_KEY` | 空 | 主密钥的十六进制串；空则去密钥文件里读 |
| `CONGORAG_MASTER_KEY_PATH` | `./data/master.key` | 主密钥文件路径 |
| `CONGORAG_TIKTOKEN_CACHE_DIR` | `./data/tiktoken-cache` | tiktoken 词表缓存目录 |
| `CONGORAG_MAX_UPLOAD_BYTES` | `33554432`（32 MiB） | 单次上传的请求体上限。写 0 或负数会被当成没设、退回默认值 |

主密钥有三个来源，按顺序解析：环境变量非空则直接用；否则读 `master.key` 文件；
文件不存在就随机生成一个写入该路径（权限 `0600`），首次启动后固定下来。
`make dev` 通过 make 的 `export` 注入 `CONGORAG_DB_URL`，所以在 `cmd.exe` 和
`bash` 里都能跑。

首次启动需要能访问一次 `openaipublic.blob.core.windows.net`：tiktoken 词表在启动时
预热，失败就拒绝启动（而不是起来之后每条消息都报错）。缓存落在
`CONGORAG_TIKTOKEN_CACHE_DIR`，只需要成功下载一次。

## API

契约在 [`contracts/openapi.yaml`](contracts/openapi.yaml)，它是唯一真相：Go 的接口和
TS 的类型都从它生成。下面是端点总览。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活检查（不碰数据库） |
| GET | `/readyz` | 就绪检查（探数据库，不可用时 503） |
| GET / POST | `/api/v1/knowledge-bases` | 知识库列表 / 新建 |
| GET / PATCH / DELETE | `/api/v1/knowledge-bases/{id}` | 详情 / 改名 / 删除 |
| GET / POST | `/api/v1/knowledge-bases/{id}/documents` | 文档列表（分页）/ 上传 |
| POST | `/api/v1/knowledge-bases/{id}/reindex` | 重新索引整个知识库 |
| POST | `/api/v1/knowledge-bases/{id}/search` | 检索调试（Hit Testing）：直接看命中的分块与相似度 |
| GET / DELETE | `/api/v1/documents/{id}` | 文档详情 / 删除 |
| POST | `/api/v1/documents/{id}/reindex` | 重新索引单份文档 |
| GET / POST | `/api/v1/conversations` | 会话列表（按最近活动时间倒序、分页）/ 新建会话 |
| GET / POST | `/api/v1/conversations/{id}/messages` | 消息列表 / 发消息 |
| GET | `/api/v1/conversations/{id}/events` | 补发 `after_event_id` 之后的事件，补完即结束响应（不持有连接） |
| GET / POST | `/api/v1/providers` | 模型服务配置 |
| GET | `/api/v1/usage` | 按模型聚合的 token 用量 |
| GET | `/api/v1/tools` | 工具目录 |
| GET / POST | `/api/v1/agents` | Agent 列表 / 新建 |
| GET | `/api/v1/agents/{id}` | Agent 详情 |
| GET / POST | `/api/v1/agents/{id}/runs` | 运行列表（分页）/ 发起运行（接受 `Idempotency-Key`） |
| GET | `/api/v1/runs/{runId}/events` | 补发这条 run 的事件（run 维度的断线重订阅），补完即结束响应 |
| POST | `/api/v1/runs/{runId}/cancel` | 取消一次在途运行（终态是 `cancelled` 而不是 `failed`） |
| POST | `/api/v1/runs/{runId}/resume` | 从断点恢复一次被中断的运行 |
| GET | `/api/v1/runs/{runId}/steps` | 运行轨迹 |

Agent 那条流的**首帧永远是 `run_started`**（`data: {"runId": ...}`），新建与命中幂等键
重放都会发——取消、重订阅、跳转运行详情都要用这个 id。

错误响应是 `application/problem+json`，形状见契约里的 `Problem` 定义。

三个会无界增长的列表端点（会话消息、知识库下的文档、Agent 运行历史）用 keyset
游标分页：请求带 `limit` / `cursor`，响应是 `{items, nextCursor}`。`nextCursor` 为
`null` 表示到底了。**游标是不透明的**——不要解析它，格式随时可能变（见契约里
`Cursor` 参数的描述）。

契约的 `info.version` 跟产品版本走（当前 `3.0` ↔ tag `v3.0`），由发布脚本在发布前
断言两者一致，见 [ADR-002](docs/adr/002-contract-versioning.md)。它不表示兼容性
承诺——**兼容边界是路径里的 `/api/v1`**。

## 开发

`make help` 列出全部命令。日常最常用的几条：

| 命令 | 说明 |
| --- | --- |
| `make up` / `make down` | 起停 PostgreSQL（`down` 不删数据卷） |
| `make psql` | 连上数据库 |
| `make dev` | 起 api（`:3210`），**不构建、不监听前端源码** |
| `make dev-web` | 起 Vite 开发服务器（`:5173`，有 HMR） |
| `make dev-worker` | 起 worker，处理文档上传 |
| `make generate` | 从契约重新生成 Go 和 TS 两端代码 |
| `make check` | build-go + vet + test + lint |

改前端要开两个终端：一个 `make dev`，一个 `make dev-web`，浏览器开 `:5173`。
`:3210` 提供的是 `go:embed` 进去的**构建产物**——改了 `web/src/` 不重新
`make build-web` 的话，`:3210` 上什么都不会变，而且不报错。Vite 会把 `/api`、
`/healthz` 和 `/readyz` 代理到 `:3210`，所以开发期浏览器只看到一个源。

契约是唯一真相：改接口先改 `contracts/openapi.yaml`，再 `make generate`，Go 结构体
和 TS 类型一起变。产物要提交进 git，CI 会跑 `make generate` 加 `git diff --exit-code`
检查有没有忘记重新生成。

## 测试

```bash
make test                # 等价于 go test ./... + 前端单测
make check               # build-go + vet + test + lint
make test-integration    # 集成测试：需要真实 PostgreSQL，会重建独立测试库
```

大部分测试不连数据库。需要真实 PostgreSQL 的集成测试用 `CONGORAG_TEST_DB_URL`
门控——没设这个变量就跳过，所以本地 `make test` 全绿**不代表那些 SQL 跑得起来**。
`make test-integration` 消掉的就是这个反馈延迟：它复刻 CI 的 integration job
（重建测试库 → 跑两套迁移 → 带变量跑全部测试）。

**它会动 schema，但动的是独立测试库。** `internal/llm` 的集成测试会 `ALTER` 向量
列类型（那个文件头部记录过一次真实事故：改动全局状态、副作用在测试通过之后才
暴露）。所以这条 target 跑在**每次重建的 `congorag_test` 库**上，你的开发库完全
不受影响。测试库跑完保留着便于排查，下一次跑会重建它。

先决条件是 `make up` 起的 PostgreSQL 在跑；没起的话它会给出可操作的报错。
要在本机也开竞态检测就加 `GO_TEST_FLAGS=-race`——但本机没有 gcc 时 `-race` 跑不
起来（CI 的 ubuntu runner 才有），这是它与 CI 唯一的一处差异。

只有 `evals/` 不在这条链路上：它测的是检索质量本身（语料、20 道题、指标脚本），
与 `measure_recall.sh` 测的 HNSW 近似索引误差分工不同，见 [`evals/README.md`](evals/README.md)。

CI 里有四个 job：Go 编译与单元测试（带 `-race`）、契约生成物是否最新、前端构建与
`go:embed`、以及在真实 PostgreSQL 服务上跑集成测试。见
[`.github/workflows/ci.yml`](.github/workflows/ci.yml)。

## 贡献

```bash
git clone git@github.com:XiaoleC05/CongoRAG.git
cd CongoRAG
git checkout -b fix/xxx
# 改代码 → make check → 提交
git commit -m "fix: ..."
git push -u origin fix/xxx
```

提交前请确保 `make check` 通过；改了 `contracts/openapi.yaml` 的话记得
`make generate` 并把产物一起提交。动了 SQL 的话再跑一遍 `make test-integration`
——本地 `make test` 跳过的正是那些需要真库的测试。前端改动先读
[`web/README.md`](web/README.md)。

### 改到这些地方要顺手做的事

| 改了什么 | 还要做什么 |
| --- | --- |
| `contracts/openapi.yaml` | `make generate` + 提交两个产物；CI 会检查有没有忘记 |
| 数据库 schema | `migrations/NNNN_xxx.{up,down}.sql` **一对**都要写 |
| 对外行为（端点、状态码、默认值、错误类型、环境变量） | 写进 `CHANGELOG.md` 的 `### 行为变更`，判据见该文件顶部 |
| 发布 | 见 [`docs/releasing.md`](docs/releasing.md) |

## 目录结构

```text
CongoRAG/
├── apps/
│   ├── api/                    HTTP 服务 + go:embed 的前端产物
│   │   ├── main.go             只做 go:embed 和调用 app.Run
│   │   ├── internal/api/       路由、错误、SSE、SPA；generated.go 是产物
│   │   └── internal/app/       装配根
│   └── worker/                 River 消费端：文档处理 + 孤儿文件对账
├── internal/
│   ├── domain/                 跨包的领域类型，不依赖任何其他内部包
│   ├── platform/               配置、数据库、日志、中间件、加密、调度
│   ├── llm/                    provider / model 配置与调用
│   ├── knowledge/              知识库与文档
│   ├── retrieval/              向量检索
│   ├── ctxmgr/                 上下文组装与压缩
│   ├── conversation/           会话、消息、事件、SSE 出口
│   └── agent/                  Agent 运行时与工具
├── contracts/openapi.yaml      契约的唯一真相，两端生成物的输入
├── packages/api-client/        TS 类型生成物（schema.d.ts）
├── migrations/                 业务表迁移（golang-migrate）
├── deployments/docker/         开发期依赖服务的 compose
├── docs/                       ADR、流式协议、测试与发布约定
├── evals/                      检索质量的测量脚本
├── web/                        React SPA 源码，规范见 web/README.md
└── Makefile                    所有开发命令的入口
```

## License

MIT，见 [LICENSE](LICENSE)。
