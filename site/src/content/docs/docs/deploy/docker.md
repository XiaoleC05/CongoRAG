---
title: Docker 交付形态
description: 用仓库里的 Dockerfile 和 compose 跑 api、worker、migrate 和 PostgreSQL。
---

仓库里有两套 compose，别拿错：

- `deployments/docker/docker-compose.yml`：开发期依赖，只有 PostgreSQL。
- `deployments/startup/docker-compose.yml`：交付形态，postgres + migrate + api + worker 四个服务。

## 当前没有预构建镜像

Release 目前没有附带镜像或启动包。你要自己构建三个镜像：

```bash
docker build --target api     -t congorag-api:5.0.2 .
docker build --target worker  -t congorag-worker:5.0.2 .
docker build --target migrate -t congorag-migrate:5.0.2 .
```

构建要拉 Node、Go、Alpine 的基础镜像，第一次会慢。构建完成后，镜像里已经包含前端产物和迁移文件。

## 配置

```bash
cd deployments/startup
cp .env.example .env
```

至少改这几项：

- `CONGORAG_VERSION=5.0.2`
- `CONGORAG_PG_PASSWORD`：改成你自己的密码
- `CONGORAG_PORT`：宿主机端口，默认 3210
- `CONGORAG_DB_URL`：保持默认即可，容器之间用服务名 `postgres`

`.env` 里不要出现公网地址。compose 默认把 api 端口绑在 `127.0.0.1`。

## 启动

```bash
docker compose -f deployments/startup/docker-compose.yml up -d
```

`congorag-migrate` 是一次性容器，跑完以 0 退出，这是正常的。`congorag-api` 要等到 healthy；`congorag-worker` 不提供 HTTP，只要在消费就是正常的。

打开 <http://127.0.0.1:3210>。

## 数据

- 数据库卷：`congorag_congorag-data` 是文档、主密钥和 tiktoken 缓存的卷；PostgreSQL 用另一个命名卷。
- 备份时数据库和数据卷要一起备。
- `docker compose down` 保留卷；`docker compose down -v` 会删掉数据。

## 启动包

仓库里有 `scripts/startup-package.mjs`，用来把三个镜像 save 成离线 tar 包，并和 compose、`.env.example`、升级脚本放在一起。具体参数看脚本头部注释。

这个脚本不会替你发布到 GitHub Release。发布产物需要你自己挂上去，或者用仓库的 release workflow。

## 注意

- 如果本机已经有一个叫 `congorag-postgres` 的容器（比如开发期的），先停掉，否则容器名冲突。
- api 容器内监听 `0.0.0.0:3210`，但宿主机端口默认只绑 `127.0.0.1`。不要为了“让外面能访问”把 ports 改成 `3210:3210`；先读反向代理那篇。
- 升级不要直接 `docker compose up -d` 换 tag。先看[升级与回滚](/docs/deploy/upgrade/)。
