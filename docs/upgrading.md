# 升级与回滚（用户视角）

这份文档是给**用启动包跑 ConGoRAG 的人**看的：怎么从 3.0 升到 3.1，出问题了
怎么退回去。

发布者（维护者）的流程在 [`releasing.md`](releasing.md)——那是另一件事：
那边讲的是"怎么把一个版本发出去"，这边讲的是"怎么把一个已经发出去的版本装起来"。
两份文档混在一起写过一次，结果是发布者以为用户会自己跑迁移，而用户以为
升级是自动的。

## 升级为什么不是"换个镜像重启一下"

两个理由，都不是可选的：

**一、升级必须跑迁移。** v3.0 的 CHANGELOG 写明「升级前必须先跑
`make migrate-up`」——漏跑 0006 的直接后果是聊天整条路径 500。数据库 schema
不会自己跟着镜像走。

**二、迁移是最容易丢数据的时刻，而它不可逆。** 所以"先备份、备份成功才迁移"
不能是一条写给人看的注意事项：人会累、会跳步骤，而脚本不会。§11 的陷阱表
第一条就是「备份失败仍继续迁移 | 升级失败没退路」。

`upgrade.mjs` 存在的全部意义就是把这个顺序变成不可跳过的。

## 升级

### 1. 下载新版本的启动包

从 GitHub Release 页下载 `congorag-startup-<版本>-linux-<架构>.zip`。
架构按 `docker info --format "{{.Architecture}}"` 判断：`x86_64` 拿 amd64，
`aarch64` / `arm64` 拿 arm64。

### 2. 导入新镜像

```bash
docker load -i images/congorag-images-linux-amd64.tar.gz
```

**这一步必须在升级之前。** 启动包是离线分发的，没有 registry 可以拉；镜像
不在本地的话 compose 会以 `image congorag-api:3.1 not found` 失败，而那句话
不会告诉你"你还没 load"。

`upgrade.mjs` 会主动检查这一点，并且在缺镜像时直接停下来——所以即使漏了，
也不会走到"迁移跑完了、服务起不来"那个最难收拾的状态。

### 3. 把 compose 与脚本更新到新包的版本

用新包里的 `docker-compose.yml`、`upgrade.mjs`、`rollback.mjs` 覆盖旧的。
**`.env` 不要覆盖**——那是你配的东西。新版本新增了环境变量的话，
`.env.example` 里会有，对照着补（脚本读不到某个变量时会告诉你）。

> 仓库里跑的话，这几样就是 `deployments/startup/docker-compose.yml` 和
> `scripts/` 下的三个文件，`git pull` 之后它们的路径分别是
> `--dir deployments/startup`（其余命令里的 `--dir .` 换成它）。

### 4. 改 `.env` 里的版本号

```
CONGORAG_VERSION=3.1
```

这一行是**目标**版本，不是当前版本。脚本从"正在跑的容器"读当前版本，
所以你把这一行改成新号这件事不会让脚本误判。

### 5. 先看一眼，再动手

```bash
node upgrade.mjs --dir . --dry-run
```

它会打印将要执行的确切命令、备份会写到哪里、以及回滚命令长什么样。

### 6. 升级

```bash
node upgrade.mjs --dir .
```

脚本按顺序做五件事，**前四件成功之前不会做第五件**：

| # | 做什么 | 失败时会怎样 |
| --- | --- | --- |
| 1 | `pg_dump -Fc` 走容器备份数据库，**并且在写盘前校验输出是 PGDMP 格式** | 中止。没有跑迁移、没有动容器 |
| 2 | 打包数据卷（原始文件 + 主密钥 + tiktoken 缓存），源卷以只读挂载 | 中止。同上 |
| 3 | 写 `manifest.json`，记下两个文件的 sha256、表数量、当前版本、卷名 | 中止。同上 |
| 4 | `docker compose run --rm migrate`（两套迁移：业务表 + River 队列表） | 中止。**容器还是旧镜像，环境仍是升级前的状态**，排查完重跑即可 |
| 5 | `docker compose up -d --wait` 切到新镜像 | 会明确告诉你"库是新 schema、容器是旧镜像"，并给出回滚命令 |

最后打印的那句「确认没问题之前不要删备份目录」不是客套：只有你看过界面、
点开过一份文档之后，这次升级才算真的成功。

### 7. 确认

- 打开 <http://127.0.0.1:3210>，能进。
- 知识库列表还在，点开一份文档能看到分块。
- `docker compose ps` 里 `congorag-api` 是 `(healthy)`。
- `docker compose logs worker` 没有持续的报错。

## 回滚

```bash
node rollback.mjs --dir . --dry-run          # 看计划，不动数据
node rollback.mjs --dir . --yes              # 真的回滚
```

不带 `--backup` 时取 `backups/` 下最新的那一份。

**回滚会丢数据**，这是它的定义而不是缺陷：数据库是
`DROP DATABASE` → `CREATE` → `pg_restore`，所以迁移之后新产生的数据
（新会话、新文档、新向量）会消失。脚本因此在 `--yes` 之前什么都不做。

回滚做三件事，**必须一起做**：

| # | 做什么 | 只做一半会怎样 |
| --- | --- | --- |
| 1 | 还原数据库（DROP + CREATE + pg_restore） | —— |
| 2 | 清空数据卷并解包回去 | 只还原数据库 → 文档记录指向已经不存在的原始文件 |
| 3 | 把 `.env` 的 `CONGORAG_VERSION` 改回备份时那个号，重启 | 只退镜像 → 旧二进制对着新 schema |

回滚前脚本会**重新校验两个备份文件的 sha256**。备份是在灾难发生之前做的，
中间有很长时间可以被各种东西损坏（磁盘、同步盘、手工挪动、拷贝中断），
而回滚是最后一次机会——这时才发现备份是坏的，等于没有备份。

## 备份里到底有什么

`backups/<时间戳>/` 下的三样：

- `database.dump` —— `pg_dump -Fc` 的自定义格式。带完整性校验、压缩过，
  用 `pg_restore -l` 可以列出内容而不能直接看文本。**向量和分块都在这里。**
- `data-volume.tar.gz` —— 整个 `/data` 卷：`documents/`（原始文件）、
  `master.key`、`tiktoken-cache/`。
- `manifest.json` —— 上面两个文件的 sha256 与大小、备份时的版本、卷名、
  容器名。**回滚脚本读它**，所以别单独删它。

### 为什么备份的是整个卷，而不是只备份 `data/documents`

issue #74 的原文写的是"另加 data/documents 打包"，实际做的是整个 `/data`。
多出来的两样东西里，**主密钥**是关键：库里所有 provider 的 API Key 都是用
它加密的。备份里只有数据库、没有主密钥的话，恢复出来是"数据都在，但每个
模型都报认证失败"——而且这个现象不会让人联想到"少备份了一个文件"。

只备份数据库、不备份原始文件同样致命，而且是 §11 陷阱表里点名的那一条：
**向量和分块在库里、原始文件在卷里，两者是配对的**；只备一半的结果是
文档列表还在、点开报错。

## 手工升级（不想装 Node 时）

脚本里那几条命令没有魔法，可以自己敲。**顺序就是全部**：

```bash
# 1. 备份数据库（宿主机上没有 pg_dump，走容器）
docker exec congorag-postgres pg_dump -U postgres -d congorag -Fc > database.dump

# 2. 确认备份真的能用——不是"文件存在"，是"格式对、有内容"
head -c 5 database.dump           # 必须是 PGDMP
ls -l database.dump               # 大小要合理，不能是 0

# 3. 备份数据卷
docker run --rm -v congorag_congorag-data:/data:ro -v "$PWD":/backup \
  alpine:3.22 tar czf /backup/data-volume.tar.gz -C /data .

# 4. 迁移（新镜像已经 load 进来之后）
docker compose run --rm migrate

# 5. 切镜像
docker compose up -d --wait
```

第 2 步是脚本有、手工容易跳过的那一步，而它恰好是最值钱的一步：一个 0 字节
的备份和没有备份，在你想用它的那一刻是同一件事。

回滚同理：`docker exec congorag-postgres psql -U postgres -d postgres -c "DROP DATABASE congorag"`、
`CREATE DATABASE congorag`、`pg_restore`，然后清空卷解包，最后把 `.env` 的
版本号改回去。**注意 `docker compose stop api worker` 要在 DROP 之前做**——
有活动连接时 DROP DATABASE 会失败。

## 出问题时先看哪里

| 现象 | 先看 |
| --- | --- |
| `image congorag-api:3.1 not found` | 镜像没 load，或者 `.env` 的 `CONGORAG_VERSION` 和实际导入的 tag 不一致（`docker images \| grep congorag`） |
| 升级后聊天整条 500 | 迁移没跑完。`docker compose run --rm migrate` 再跑一次（幂等） |
| `database congorag is being accessed by other users` | 还有连接连着库。`docker compose stop api worker`，必要时连带掐掉自己开的 psql |
| api 容器起来了但外面连不进来 | `docker-compose.yml` 里 `CONGORAG_LISTEN_ADDR` 是不是 `0.0.0.0:3210`（见启动包的 README） |
| 每个模型都报认证失败 | 数据卷里的主密钥和数据库对不上——多半是只还原了数据库没还原卷 |
