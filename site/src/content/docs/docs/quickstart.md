---
title: 快速开始
description: 从源码把 CongoRAG 跑起来，并完成第一次模型配置。
---

这篇文档只做一件事：让你在本地打开 CongoRAG 的界面，并且能问出第一个带引用的问题。

## 你需要什么

- Go 1.26.4（版本写在仓库的 go.mod 里）
- Docker，带 `docker compose` 子命令
- Node.js 22 和 pnpm 11（构建前端用）
- make
- 能访问 GitHub 和模型服务 API 的网络

第一次启动还需要访问一次 `openaipublic.blob.core.windows.net` 下载 tiktoken 词表。下载失败时进程会拒绝启动，不会带病运行。

## 1. 克隆并安装命令行工具

```bash
git clone git@github.com:XiaoleC05/CongoRAG.git
cd CongoRAG

go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1
go install github.com/riverqueue/river/cmd/river@v0.47.0
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
```

Makefile 会把 `$(go env GOPATH)/bin` 拼进 PATH，所以这三条装完不用再改环境变量。

## 2. 起数据库并建表

```bash
make up
make migrate-up
make river-migrate-up
```

`make up` 起的是 `pgvector/pgvector:0.8.6-pg18`，只绑 `127.0.0.1:5432`。

两套迁移都要跑：业务表归 golang-migrate，River 的队列表归 river。只跑一套的话，另一套对应的功能会报错。

## 3. 构建并启动

```bash
make build
make dev
```

`make build` 会先构建前端，再把它 `go:embed` 进 api 二进制。打开 <http://127.0.0.1:3210>。

再开一个终端跑 worker：

```bash
make dev-worker
```

没有 worker 时，上传的文档会一直停在待处理状态。

## 4. 配置模型

第一次打开会进入引导页。需要填：

- 模型服务的 base URL，例如 `https://api.siliconflow.cn/v1`
- API Key
- 一个 chat 模型名
- 一个 embedding 模型名
- chat 模型是否支持工具调用

CongoRAG 走的是 OpenAI 兼容的 chat / embedding 接口。Base URL 要写到 `/v1` 这一层，具体以你的服务商文档为准。

embedding 模型会在这一步发一次真实请求，量出向量维度。维度决定数据库里向量列的形状，之后换 embedding 模型需要重建索引。

## 怎么确认成功

- `curl -s http://127.0.0.1:3210/healthz` 返回 200。
- 浏览器能打开引导页，填完模型后能进入知识库页。
- `make psql` 能连上数据库，`\dt` 能看到业务表。
- 上传一份 `.md` 或 `.txt` 文档后，worker 终端有处理日志，页面上的状态从 queued 变成 ready。

## 常见失败

**端口 5432 被占用。** 本机已经有 PostgreSQL 在跑。停掉它，或者改 `deployments/docker/docker-compose.yml` 的宿主机端口，同时改 `CONGORAG_DB_URL`。

**`migrate: command not found`。** 第 1 步的 `go install` 没做，或者 GOPATH/bin 不在 PATH。Makefile 会自己拼，直接从仓库根跑 `make migrate-up` 通常能解决。

**启动时卡在 tiktoken。** 这台机器访问不了那个地址。先确认网络，再重试；不要在下载失败的情况下继续启动。

**页面能开，但上传后一直 queued。** worker 没起，或者 worker 连的不是同一个数据库。看 worker 终端的报错。

**Windows。** 需要可用的 make。仓库没有提供 Windows 安装包；命令本身是 make 目标，能在 cmd.exe 和 Git Bash 里跑，但 make 要你自己装。

## 下一步

- [配置](/docs/configuration/)：环境变量、主密钥、上传上限。
- [接入模型](/docs/guides/models/)：模型能力、embedding 维度、换模型。
- [知识库与文档](/docs/guides/knowledge-bases/)：支持的文件类型和处理流程。
