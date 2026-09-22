# ConGoRAG 启动包

这个包里是四个已经打包好的镜像和一份 compose 文件。**不需要装 Go、不需要
装 Node、不需要在本机编译**——只要能跑 Docker。

## 需要什么

- Docker Engine 或 Docker Desktop（要带 `docker compose` 子命令，即 Compose v2）
- 磁盘：镜像约 300 MB，加上你的文档和向量库
- 一个能出网的环境，用来调模型 API（模型在界面上配，见下面第 4 步）

## 起起来（三步）

```bash
# 1. 导入镜像。文件名里的架构要和你的机器一致：
#    x86_64 / Intel 芯片 → amd64；Apple Silicon / ARM 服务器 → arm64
docker load -i images/congorag-images-linux-amd64.tar.gz

# 2. 配环境变量
cp .env.example .env

# 3. 起
docker compose up -d
```

然后打开 <http://127.0.0.1:3210>。

`docker compose ps` 应该看到四个服务，其中 `congorag-migrate` 是 `Exited (0)`
——它是**一次性**的迁移容器，跑完就退出，那是正常的，不是崩溃。
`congorag-api` 要等到显示 `(healthy)` 才真的可用（首次约十几秒）。

## 第一步之后要做的事

### 4. 配模型

界面会引导你走这一步。需要填的是：API 地址（例如 `https://api.siliconflow.cn/v1`）、
API Key、以及两个模型名——一个 chat 模型（要支持工具调用）和一个 embedding 模型
（维度必须和已有的向量一致，否则检索会报错）。

这些值存在数据库里，API Key 用主密钥加密。主密钥在数据卷里（`/data/master.key`），
**它丢了的话那些 Key 全部作废**——见下面的「备份」。

### 5. 上传文档

上传之后文档是异步处理的（parse → chunk → embed → index），由 `congorag-worker`
消费。上传完没反应的话先看 `docker compose logs worker`——最常见的原因是模型
还没有配好。

## 备份

要备的东西有**两样**，缺一样就会得到"文档列表还在、点开报错"这种半坏的状态：

```bash
# 数据库
docker exec congorag-postgres pg_dump -U postgres -d congorag -Fc > database.dump

# 数据卷（原始文件 + 主密钥 + tiktoken 缓存）
docker run --rm -v congorag_congorag-data:/data:ro -v "$PWD":/backup \
  alpine:3.22 tar czf /backup/data-volume.tar.gz -C /data .
```

只备数据库的话，向量和分块都在、原始文件没了；只备文件的话，反过来。
升级脚本会自动把两份一起备，见 `docs/upgrading.md`。

## 升级 / 回滚

```bash
node upgrade.mjs --dir . --dry-run   # 先看一眼要做什么
node upgrade.mjs --dir .
```

`upgrade.mjs` 会先备份、**校验备份可用之后**才跑迁移，任一步失败即中止。
回滚用 `node rollback.mjs --dir . --yes`。完整步骤与它为什么这么排见
`docs/upgrading.md`。

> 升级脚本是 `.mjs`，需要宿主机有 **Node.js ≥ 22**。这是包里唯一一个
> 运行时要求——不想装 Node 的话，可以照 `docs/upgrading.md` 的「手工升级」
> 一节自己敲那几条命令，效果一样，只是没有"备份成功才迁移"这层保证。

## 常用命令

```bash
docker compose ps                  # 看状态（api 要 healthy）
docker compose logs -f api         # 看 api 日志
docker compose logs -f worker      # 文档不处理时看这个
docker compose down                # 停（数据保留）
docker compose down -v             # 停并**删除所有数据**，慎用
docker compose exec postgres psql -U postgres -d congorag   # 连库排障
```

## 连不进来 / 打开是空白

按这两条查，它们覆盖了绝大多数情况：

1. **容器起来了、日志也打印了 listening，但外面连接被拒。**
   检查 `docker-compose.yml` 里 api 的 `CONGORAG_LISTEN_ADDR` 是不是
   `0.0.0.0:3210`。程序的默认值是 `127.0.0.1:3210`（安全考虑），容器里用
   回环地址的话宿主机的端口映射转发不进来——而**没有任何报错**。
   这一项在 compose 和 `.env.example` 里都写着，别删。

2. **健康检查通过，但页面空白。**
   说明 api 二进制里的前端产物是空的——正常发布的镜像不会这样（构建期有一道
   断言拦它）。先 `docker compose logs api` 看有没有 `MountSPA` 的警告；
   再确认你导入的镜像和 compose 里写的 tag 一致（`docker images | grep congorag`）。

## 这个包和仓库的关系

包里的 `docker-compose.yml` 来自仓库的 `deployments/startup/docker-compose.yml`，
升级脚本来自 `scripts/`。仓库里还有一套 `deployments/docker/docker-compose.yml`
——那是**开发期**用的，只有 PostgreSQL 一个服务，api 跑在宿主机上。两个文件
不要混用。
