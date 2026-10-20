---
title: 升级与回滚
description: 备份、迁移、换版本，以及回滚会丢什么。
---

升级不是“换个二进制重启一下”。数据库 schema 要跟着走，而迁移是最容易丢数据的时刻。

## 先备份

源码形态要备两样：

1. 数据库。
2. 数据目录：原始文档、`master.key`、tiktoken 缓存。

```bash
docker exec congorag-postgres pg_dump -U postgres -d congorag -Fc > database.dump
tar czf data.tar.gz -C data .
```

Docker 形态用数据卷：

```bash
docker exec congorag-postgres pg_dump -U postgres -d congorag -Fc > database.dump
docker run --rm -v congorag_congorag-data:/data:ro -v "$PWD":/backup \
  alpine:3.22 tar czf /backup/data-volume.tar.gz -C /data .
```

只备数据库、不备数据卷，恢复后文档记录还在、原始文件没了；只备数据卷、不备数据库，反过来。主密钥丢了，所有 API Key 都要重填。

## 源码升级

```bash
git pull
make migrate-up
make river-migrate-up
make build
```

然后重启 api 和 worker。前端有改动时，`make build` 会重新构建并 embed；只改后端时也会重新构建，只是前端产物没变。

迁移失败就停在原地，不要继续换二进制。先看迁移的报错，修好再重跑。

## Docker 升级

仓库里的 `scripts/upgrade.mjs` 会按顺序做：

1. 备份数据库，并校验输出是 PGDMP 格式。
2. 打包数据卷。
3. 写 manifest，记录 sha256、表数量、当前版本。
4. 跑迁移。
5. 切到新镜像。

前四步成功之前不会做第五步。

```bash
node upgrade.mjs --dir deployments/startup --dry-run
node upgrade.mjs --dir deployments/startup
```

升级前把 `.env` 里的 `CONGORAG_VERSION` 改成目标版本，并确认新镜像已经 `docker load` 或 build 过。

## 回滚

```bash
node rollback.mjs --dir deployments/startup --dry-run
node rollback.mjs --dir deployments/startup --yes
```

回滚会 **DROP 数据库再恢复**，所以备份之后产生的数据会消失。这是回滚的定义，不是 bug。

回滚会一起做三件事：还原数据库、还原数据卷、把 `CONGORAG_VERSION` 改回备份时的版本。只做一半会得到“文档记录指向不存在的文件”或“旧二进制对着新 schema”。

## 手工升级

不想用脚本时，顺序是：

1. 停 api 和 worker。
2. 备份数据库和数据卷/目录。
3. 跑 `migrate` 和 `river` 两套迁移。
4. 换二进制/镜像。
5. 起服务，确认 `/healthz` 和一份文档能打开。

不要在迁移没跑完时启动新版本；也不要在备份没校验时跑迁移。
