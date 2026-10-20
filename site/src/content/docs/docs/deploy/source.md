---
title: 从源码运行
description: 不用 Docker 交付形态，直接构建 api 和 worker 两个二进制。
---

这条路径适合想改代码、或者不想在机器上跑 Docker 的人。前提是你已经按[快速开始](/docs/quickstart/)起好了 PostgreSQL 并跑完迁移。

## 构建

前端产物要先构建，因为 api 用 `go:embed` 把它编进二进制：

```bash
make build-web

mkdir -p bin
go build -o bin/api ./apps/api
go build -o bin/worker ./apps/worker
```

Windows 下产物是 `bin\api.exe` 和 `bin\worker.exe`，命令里的路径分隔符换成反斜杠即可。

不要跳过 `make build-web`。直接 `go build` 时，`apps/api/web/` 里可能只有占位文件，编出来的二进制能启动，但界面是空的；构建脚本里的断言就是为了提前拦住这种情况。

## 启动

两个进程都要连同一个数据库：

```bash
export CONGORAG_DB_URL='postgres://postgres:postgres@127.0.0.1:5432/congorag?sslmode=disable'
export CONGORAG_DOCUMENTS_DIR='./data/documents'

./bin/api
```

另一个终端：

```bash
export CONGORAG_DB_URL='postgres://postgres:postgres@127.0.0.1:5432/congorag?sslmode=disable'
export CONGORAG_DOCUMENTS_DIR='./data/documents'

./bin/worker
```

Windows PowerShell 用 `$env:CONGORAG_DB_URL='...'` 设置变量。

api 默认监听 `127.0.0.1:3210`。worker 不提供 HTTP，只消费 River 队列。

## 目录与权限

默认工作目录下会生成：

- `data/documents/`：上传的原始文件
- `data/master.key`：主密钥，权限 0600
- `data/tiktoken-cache/`：tiktoken 词表缓存

`data/` 已经在 .gitignore 里。不要把它提交进仓库，也不要只备份数据库而漏掉这个目录。

## systemd 示例

如果要在 Linux 上常驻，两个进程各一个 unit。下面只给骨架，路径和用户按你的机器改：

```ini
[Unit]
Description=CongoRAG api
After=network.target postgresql.service

[Service]
Type=simple
User=congorag
WorkingDirectory=/opt/congorag
EnvironmentFile=/etc/congorag/env
ExecStart=/opt/congorag/bin/api
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

worker 的 unit 把 `ExecStart` 换成 `/opt/congorag/bin/worker`，其余相同。`/etc/congorag/env` 里放 `CONGORAG_DB_URL` 和 `CONGORAG_DOCUMENTS_DIR`，权限设成 0600。

## 注意

- 不要把 api 直接绑到 `0.0.0.0` 后暴露到公网。CongoRAG 没有认证。
- 升级时先备份数据库和 `data/`，再跑迁移，最后换二进制。顺序见[升级与回滚](/docs/deploy/upgrade/)。
- 日志默认输出到 stdout，交给 systemd/journald 或你的进程管理器收集。
