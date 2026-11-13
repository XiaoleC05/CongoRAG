# 部署 runbook（生产 / 演示 / 本地复现）

- 适用版本：v6.5.0 起的交付形态（写作时契约版本 v6.5.0，以 `contracts/openapi.yaml` 的 `info.version` 为准；版本规范见 ADR-002）
- 读者：部署/运维评审环境的人，以及要复现"从零到可用"的答辩观众
- 相关文档：[启动包用户手册](../deployments/startup/README.md)、[演示部署](../deployments/production/README.md)、[升级回滚用户视角](upgrading.md)、[发布流程](releasing.md)、[系统架构](architecture.md)
- 命令默认在**仓库根目录**执行；在启动包目录执行时会单独标注

本文只写"仓库里真的存在、能被核对"的命令与配置。

## 1. 部署形态

仓库里有三份 compose，用途不同、**不要混用**（同时起会抢容器名与端口）：

| 文件 | 用途 | 服务 | 镜像 tag |
| --- | --- | --- | --- |
| `deployments/docker/docker-compose.yml` | 开发期依赖，只有 PostgreSQL | postgres | 固定镜像 |
| `deployments/startup/docker-compose.yml` | 交付/评审：用户本机，无需 Go/Node | postgres + migrate + api + worker | `CONGORAG_VERSION`（必填），从 tar 包 `docker load` |
| `deployments/production/docker-compose.yml` | 演示/公网：多一个 PDF sidecar | postgres + migrate + pdf-service + api + worker | `CONGORAG_IMAGE_TAG`（默认 `cup`） |

生产/演示的推荐形态（反代是**必要**的，原因见 §4）：

~~~mermaid
flowchart LR
  B[浏览器] -->|HTTPS + Basic Auth| RP[反向代理<br/>Caddy 或 nginx]
  RP -->|127.0.0.1:3210| API[api 进程<br/>HTTP + SSE + 内嵌 SPA]
  API --> PG[(PostgreSQL 18 + pgvector<br/>业务表 + 向量 + River 队列)]
  W[worker 进程] --> PG
  API --> PDF[pdf-service<br/>FastAPI /parse]
  W --> PDF
  API -.BYOK.-> LLM[模型服务<br/>OpenAI 兼容 API]
  W -.embedding.-> LLM
~~~

进程职责（装配根 `apps/api/internal/app/app.go`、`apps/worker/internal/app/app.go`）：

- **api**：唯一 HTTP/SSE 出口。前端产物在**编译期**通过 `go:embed` 嵌进二进制；同时跑对话、检索、Agent 执行、BYOK 配置、用量统计。
- **worker**：River 消费端，无 HTTP、无端口。做文档处理流水线（解析 → 切分 → 指纹去重 → embedding → 索引）与全部周期维护任务（会话摘要、偏好抽取、事件/checkpoint 回收、孤儿文件对账）。
- **pdf-service**：PDF 学术解析 sidecar，`POST /parse` 返回带页码/坐标/章节的块。缺它时 `.pdf` 上传会在处理阶段落 `failed`（md/txt 不受影响）。
- **migrate**：一次性容器，跑完两套迁移即退出（`Exited (0)` 正常，不是崩溃）。

本地开发 vs 生产：

| 维度 | 本地开发 | 生产/演示 |
| --- | --- | --- |
| 起法 | `make up`（只起 PostgreSQL）+ `make dev` / `make dev-worker` + `make dev-web`（Vite :5173） | `docker compose up -d`，四/五个容器 |
| api 监听 | 默认 `127.0.0.1:3210` | 容器内显式 `0.0.0.0:3210`，宿主机映射绑 `127.0.0.1` |
| 前端 | Vite HMR，浏览器开 :5173（代理 /api 到 :3210） | api 内嵌构建产物，浏览器开 :3210 |
| 配置 | 仓库根 `.env.example`（**程序不自动加载 .env**） | 对应 deployments 目录下的 `.env`（compose 自动读） |
| 数据 | `./data/`（gitignore） | 命名卷 `congorag-data`（/data）+ `pgdata` |
| 反代 | 不需要 | 必须：TLS、Basic Auth、SSE 相关头（§4） |

## 2. 配置

全部由 `internal/platform/config.go` 的 `LoadConfig` 从环境变量读取。**只有 `CONGORAG_DB_URL` 必填**，其余有默认值。

| 变量 | 归属 | 默认 | 说明 / 配错会怎样 |
| --- | --- | --- | --- |
| `CONGORAG_DB_URL` | api + worker + migrate | 无（必填） | 空 → 启动直接报 `CONGORAG_DB_URL is required`。容器间必须用 compose 服务名 `postgres`；写 127.0.0.1 会连容器自己，表现为 connection refused。 |
| `CONGORAG_LISTEN_ADDR` | api | `127.0.0.1:3210` | 容器内必须覆盖成 `0.0.0.0:3210`，否则宿主机端口映射进不来且**没有任何报错**（只有日志里的 listening 地址看得出来）。 |
| `CONGORAG_DOCUMENTS_DIR` | api + worker | `./data/documents` | 镜像里 Dockerfile 已设 `/data/documents`。api 写、worker 读，必须同一个卷，否则上传成功、处理永远 `queued`。 |
| `CONGORAG_MASTER_KEY` | api | 空 | 主密钥十六进制串（AES-256 需 32 字节），见"三级来源"。格式错 → 启动失败。 |
| `CONGORAG_MASTER_KEY_PATH` | api | `./data/master.key` | 镜像里 `/data/master.key`。必须与数据卷一起备份：丢了它，库里加密的 provider key 全部作废。 |
| `CONGORAG_TIKTOKEN_CACHE_DIR` | api + worker | `./data/tiktoken-cache` | 镜像里 `/data/tiktoken-cache`。首次加载 encoding 要联网拉词表，之后走缓存。 |
| `CONGORAG_MAX_UPLOAD_BYTES` | api | 33554432（32 MiB） | 上传请求体上限。**0 或非法值退回默认值，不会变成"不限"**。 |
| `CONGORAG_PDF_SERVICE_URL` | api + worker | 空 | 空 = 关闭 PDF 通道：`.pdf` 上传在处理阶段落 failed（提示没有配置解析器），md/txt 不受影响。 |
| `CONGORAG_PDF_TIMEOUT` | api + worker | `10m` | 单次 `/parse` 的 HTTP 超时。541 页实测约 2 分钟；调太小会让大文档永远重试。 |
| `CONGORAG_LOG_LEVEL` | api + worker | `info` | `debug/info/warn/error`；非法值退回 info。slog TextHandler 输出到 stdout。 |
| `CONGORAG_RETRIEVAL_DEDUP` | api + worker | 开（非 `off` 即开） | 混合检索后的近重复抑制。`off` 只用于离线评测 A/B。 |
| `CONGORAG_RATE_LIMIT_PER_MINUTE` | api | 240 | general 档令牌/分钟（所有 /api/ 请求）。0 = 关闭该档。 |
| `CONGORAG_RATE_LIMIT_BURST` | api | 60 | general 桶容量。速率>0 而 burst<1 → **启动失败**。 |
| `CONGORAG_RATE_LIMIT_LLM_PER_MINUTE` | api | 30 | expensive 档（调模型/embedding/重索引的 POST）令牌/分钟。0 = 关闭（这些端点回落 general）。 |
| `CONGORAG_RATE_LIMIT_LLM_BURST` | api | 8 | expensive 桶容量。开着但为 0 → 启动失败。 |
| `TIKTOKEN_CACHE_DIR` | api + worker | 未设（由上面 CONGORAG 项推导） | tiktoken-go 自己读的库变量，最终落点见 `internal/llm/vocab.go`。 |

限流（v6.4.0 起，`internal/platform/ratelimit.go`）：

- 负数/非整数/"速率>0 但 burst=0"都会让 **api 拒绝启动**（四项全校验）。手滑配置不能静默变成"全拒"或"不限"。
- **容器部署要改 compose，不只是 .env**：仓库自带的两份应用 compose 的 `environment:` 只透传列出的变量（没有 `env_file`）。限流四项、`CONGORAG_RETRIEVAL_DEDUP`、`CONGORAG_MASTER_KEY` 等未列出的项，加进 `.env` **不会**进容器——要在 `apps/api` 的 `environment:` 里加对应行（或给它加 `env_file: .env`），再 `docker compose up -d api`。本地 `make dev` 则直接 export 环境变量。
- 两档桶互相独立；expensive 关闭时昂贵端点回落 general；两档都 0 时 /api/ 不限流。
- 只对 `/api/` 前缀生效。静态资源、SPA 路由、/healthz、/readyz 不计入。
- 计数在**进程内存**，重启清零；不支持多副本。

主密钥三级来源（`internal/platform/secretbox.go` 的 `resolveMasterKey`）：

1. `CONGORAG_MASTER_KEY` 非空 → 按十六进制解码（必须 32 字节，否则启动失败）；
2. 否则读 `CONGORAG_MASTER_KEY_PATH` 文件里的十六进制串；
3. 否则随机生成 32 字节写回该路径（0600），之后固定。

它加密所有 provider 的 API Key（AES-GCM 密文落库）。恢复/迁移数据时，主密钥必须与数据库**一起**带走。

**模型不通过环境变量配置**：provider/base URL/Key/模型名/维度在界面「设置 → 模型」里配（BYOK）。embedding 维度由服务端探测；换 embedding 模型若会清空已有向量，接口返回 409 `embedding_change_requires_reindex`，需要用户显式确认。

## 3. 首次部署

### 3.1 启动包（评审环境首选，无需 Go/Node）

前置：Docker Engine / Docker Desktop（Compose v2）。**升级脚本另需宿主机 Node.js ≥ 22**。

~~~bash
docker load -i images/congorag-images-linux-amd64.tar.gz   # 架构按 docker info --format '{{.Architecture}}' 选
cp .env.example .env
docker compose up -d
docker compose ps          # migrate 应为 Exited (0)，api 最终 (healthy)
docker compose logs migrate
~~~

顺序由 compose 的 `depends_on` 保证：postgres(healthy) → migrate(Exited 0) → api/worker。migrate 容器依次跑**两套迁移**（`deployments/docker/migrate-entrypoint.sh`）：

1. 业务表：`migrate -path /migrations -database "$CONGORAG_DB_URL" up`（golang-migrate，`migrations/`）
2. River 队列表：`river migrate-up --database-url "$CONGORAG_DB_URL" --line main`（river CLI 内嵌迁移）

两套都幂等，重复 `docker compose up` 不会红；少跑一套的典型现象是 worker 一直报 `relation "river_job" does not exist`（ADR-003）。

打开 <http://127.0.0.1:3210> 完成 BYOK 引导。可选演示数据：

~~~bash
node scripts/seed-demo.mjs --api http://127.0.0.1:3210 --dir ./测试数据/pdf
~~~

### 3.2 从源码构建（含 PDF sidecar 的演示部署）

前置：仓库根目录、Docker、网络（构建要拉 Go/Node/镜像）。

~~~bash
docker build --target api     -t congorag-api:v6 .
docker build --target worker  -t congorag-worker:v6 .
docker build --target migrate -t congorag-migrate:v6 .

cd deployments/production
cp .env.example .env
docker compose up -d --build
docker compose ps   # 5 个容器；migrate Exited(0) 正常
~~~

与启动包的两点差异：镜像 tag 用 `CONGORAG_IMAGE_TAG`（默认 `cup`）；api/worker 的 `CONGORAG_PDF_SERVICE_URL` 固定指向 `http://pdf-service:8000`。

## 4. 反向代理（生产必读）

api 默认只监听 `127.0.0.1:3210`，且 `OriginCheck`（`internal/platform/middleware.go`）只放行**回环 Host**（防 DNS rebinding）。反代必须同时满足四件事：

1. **把 Host 改写成回环地址**，否则所有请求 403 `forbidden_host`。
2. **把 `X-Forwarded-For` 改写成客户端真实 IP（覆盖，不是追加）**。限流按 client key 分桶：直连对端是回环时取 XFF 第一跳（`internal/platform/ratelimit.go` 的 `clientKey`）。用 nginx 的 `$proxy_add_x_forwarded_for` 时最左值可被客户端伪造，轮换 XFF 即可绕过限流。
3. **关闭响应缓冲并放大读超时**，否则 SSE 会被反代攒着/掐断。服务端 v6.4.0 起每空闲 15 秒发一条注释帧 `: keep-alive`（`apps/api/internal/api/sse.go`）。
4. **请求体上限 ≥ `CONGORAG_MAX_UPLOAD_BYTES`**（默认 32 MiB），否则大 PDF 在反代就 413，业务日志看不到。

### 仓库里的真实配置：Caddy Basic Auth

`deployments/production/README.md` 给的是 Caddy（不是 nginx）：

~~~bash
docker run --rm caddy:2 caddy hash-password --plaintext '你的访问口令'
~~~

~~~text
demo.example.com {
    basic_auth {
        reviewer <bcrypt-hash>
    }
    reverse_proxy 127.0.0.1:3210
}
~~~

**注意**：Caddy 默认原样传递请求 Host。照抄到公网域名时 Host 是 `demo.example.com`，会被 OriginCheck 拒成 403。需要把 Host 改成上游地址：

~~~text
demo.example.com {
    basic_auth {
        reviewer <bcrypt-hash>
    }
    reverse_proxy 127.0.0.1:3210 {
        header_up Host {upstream_hostport}
    }
}
~~~

### 推荐的 nginx 等价配置

仓库里提供了 `deployments/production/nginx.conf.example` 作为 nginx 等价示例；下面是按上述四条约束写的等价配置：

~~~nginx
server {
    listen 443 ssl;
    server_name congorag.example.com;

    ssl_certificate     /etc/letsencrypt/live/congorag.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/congorag.example.com/privkey.pem;

    auth_basic           "CongoRAG";
    auth_basic_user_file /etc/nginx/congorag.htpasswd;   # htpasswd -c ... reviewer

    client_max_body_size 33m;   # ≥ CONGORAG_MAX_UPLOAD_BYTES

    location / {
        proxy_pass http://127.0.0.1:3210;
        proxy_http_version 1.1;

        proxy_set_header Host 127.0.0.1:3210;              # 关键 1
        proxy_set_header X-Forwarded-For $remote_addr;     # 关键 2：覆盖，不用追加式
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";

        proxy_buffering off;   # 关键 3：SSE
        proxy_cache off;
        gzip off;
        proxy_read_timeout  180s;
        proxy_send_timeout  180s;
    }
}
~~~

超时口径：SSE 空闲心跳 15s、nginx `proxy_read_timeout` 默认 60s（`docs/sse-protocol.md` 点名了这个值）。建议读超时 ≥ 120s 且大于单轮模型调用的最长静默期。**不要给 api 设 WriteTimeout**：`apps/api/internal/app/app.go` 刻意只设 ReadHeaderTimeout(10s)/ReadTimeout(5m)，写超时会掐断长 SSE。

## 5. 升级与回滚

**版本号 = 契约版本 = 产品版本**（ADR-002；当前 `contracts/openapi.yaml` 的 `info.version`）。镜像 tag 即产品版本（release.yml 用 `congorag-api:<版本>` 打包）；启动包 `.env` 用 `CONGORAG_VERSION=<版本>` 指定目标镜像。

### 升级

~~~bash
node upgrade.mjs --dir . --dry-run     # 先看计划：备份写哪、命令是什么、回滚命令长什么样
node upgrade.mjs --dir .               # 真正升级
# 仓库内等价入口：make upgrade UPGRADE_ARGS="--dry-run"
~~~

`scripts/upgrade.mjs` 的顺序就是它存在的理由（`docs/upgrading.md` §6）：① pg_dump -Fc 备份数据库并校验 PGDMP 格式 → ② 只读挂载 /data 卷打 tar.gz（原始文件+主密钥+tiktoken 缓存）→ ③ 写 manifest.json（sha256/大小/表数量/版本/卷名）→ ④ 才跑 `docker compose run --rm migrate`（两套迁移）→ ⑤ 才 `docker compose up -d --wait` 切镜像。任一步失败即中止；第 4 步失败时容器仍是旧镜像、可重跑；第 5 步失败会提示"库是新 schema、容器是旧镜像"并给出回滚命令。升级前先 `docker load` 新镜像（脚本会检查）；不要覆盖 `.env`，新变量对照新包 `.env.example` 补。

手工升级等价命令见 `docs/upgrading.md`「手工升级」：备份 → 校验（head -c 5 必须是 PGDMP）→ 迁移 → 切镜像，不能跳。

### 回滚

~~~bash
node rollback.mjs --dir . --dry-run    # 看计划
node rollback.mjs --dir . --yes        # 真正回滚（不带 --yes 只检查）
# 仓库内等价入口：make rollback ROLLBACK_ARGS="--yes"
~~~

回滚三件事必须一起做：① DROP DATABASE + CREATE + pg_restore；② 清空并解包 `congorag-data` 卷；③ `.env` 的 `CONGORAG_VERSION` 改回备份时的版本并重启。**回滚会丢数据**（迁移后新产生的会话/文档/向量消失，这是定义不是缺陷），所以要求 `--yes`。回滚前脚本会重新校验备份 sha256。契约的破坏性变更不因回滚自动兼容（ADR-002）。

## 6. 备份与恢复

要一起备**两样**，缺一样就是"数据都在、点开报错"：

~~~bash
docker exec congorag-postgres pg_dump -U postgres -d congorag -Fc > database.dump

docker run --rm -v congorag_congorag-data:/data:ro -v "$PWD":/backup \
  alpine:3.22 tar czf /backup/data-volume.tar.gz -C /data .
~~~

校验：`head -c 5 database.dump` 必须是 `PGDMP`，大小合理不为 0。恢复：**先停 api/worker**（有活动连接时 DROP DATABASE 会失败），再 DROP/CREATE/pg_restore，然后清空卷解包，最后改回版本号并 `docker compose up -d`。只恢复数据库不恢复卷 → 文档记录指向不存在的原始文件；反过来同样坏；**主密钥只恢复一半 → 每个模型都报认证失败**（provider key 解不开）。日常建议：升级前必备（脚本强制）；每季度演练一次恢复到临时目录。备份目录不要和 `pgdata`/`congorag-data` 同盘。

## 7. 监控与探针

| 探针 | 回答什么 | 实现 | 用法 |
| --- | --- | --- | --- |
| `GET /healthz` | 进程活着吗 | `apps/api/internal/api/server.go`，固定 `{"status":"ok"}` | Dockerfile HEALTHCHECK 用它（10s 间隔/3s 超时/5 次重试）。**不要**拿它判断能否发流量。 |
| `GET /readyz` | 现在能服务请求吗 | 探 PostgreSQL（2 秒超时）；失败返回 503 Problem（`type=unavailable`、detail=数据库连接不可用），并打 `readiness probe failed` Warn 日志 | 编排器/LB 的 readiness 判据；排障 `curl -s -o /dev/null -w '%{http_code}' .../readyz`。 |

**没有 Prometheus/OTel 端点**（已知限制）。可观测性靠三层：

1. **日志**：slog TextHandler → stdout（`internal/platform/logger.go`），级别由 `CONGORAG_LOG_LEVEL` 控制。业务日志带 `request_id`（响应头 `X-Request-ID` 同源）；Agent 日志还带 `agent_run_id`。关键行：`request failed`（5xx，含 error 原文）/ `request rejected`（4xx，Warn）/ `send message failed` / `agent run failed` / `agent run resume failed` / `readiness probe failed` / `panic recovered` / `failed to record agent run step`。
2. **业务接口**：`GET /api/v1/usage`（token 用量按模型聚合）、`POST /api/v1/knowledge-bases/{id}/search`（vectorScore/lexicalScore/融合分）、SSE 的 `grounding` 事件（引用落地统计）。
3. **容器/数据库**：`docker compose ps`、`docker compose logs -f api/worker`、`docker stats`、`pg_isready -U postgres -d congorag`、容器内 `psql`。

告警建议（无内置告警，用外部 cron）：/readyz 连续 3 次非 200；容器重启次数；磁盘 >85%；码头 river_job 中 available/retryable 积压 >50。

## 8. 容量与已知限制

- **边界是单机单用户**（ADR-001）：无多租户、无应用层登录；公网访问必须在反代层加 Basic Auth + TLS。
- **连接**：两个进程各一个 pgxpool，仓库未覆盖连接数（api 装配只注册 pgvector 编解码器），用 pgx 默认值（`max(4, NumCPU)`）。数据库与业务/队列同库（ADR-003）。
- **worker 并发**（`apps/worker/internal/app/app.go`）：默认队列 10 个 worker、维护队列 3 个；单文档处理上限 30 分钟（`internal/knowledge/river.go`），周期任务上限 30 分钟，优雅停机给在途任务 60 秒。
- **文档处理**：上传上限 32 MiB；分块目标上限 1200 字符；embedding 每批 256 条；PDF 解析超时默认 10 分钟。
- **检索/上下文**：混合检索（向量+词法+RRF k=60，召回倍率 4 倍、下限 20/上限 50）；聊天历史取最近 50 条后交上下文预算裁剪；单条消息上限 8000 字符。
- **Agent**：一次 run 最多 20 轮模型调用；输入上限 8000 字符；checkpoint 保留 24 小时；工具全部只读/低风险（knowledge_search/arxiv_search/calculator/conversation_search）。
- **性能参考**（引用仓库文档，非本机复测）：技术文档 §4.10 记录 10,573 分块上混合检索平均 154ms / P95 178ms（topK=5，24 次）；`docs/performance.md` 是纯函数微基准与前端产物体积基线；`docs/quality-baseline.md` 记录本机可执行/只能 CI 验证的清单。数字换机器/换库规模不可外推。
- **已知限制**：无 TLS/认证（靠反代）；无 metrics 端点；单机不横向扩展；模型质量/配额取决于 BYOK；tiktoken 词表首次加载需联网；PDF sidecar 首次构建 3~5 分钟；SSE 长连接需要正确反代配置；Windows 本机无 gcc，`-race` 只能在 CI 跑。

## 9. 故障处置 playbook

统一先做：`docker compose ps` 看容器状态，`docker compose logs --tail=200 <服务>` 看第一处报错。**不要先重启数据库**。

| 现象 | 看什么 | 怎么判断 | 怎么处置 |
| --- | --- | --- | --- |
| 页面/接口普遍失败，/readyz 503 `unavailable` | `docker compose ps postgres`/logs、api 日志 `readiness probe failed`、`pg_isready`、`CONGORAG_DB_URL` | postgres 不健康或连接串错（容器里写 127.0.0.1 是常见错） | 修 postgres/卷；确认用服务名 postgres；数据库恢复后 /readyz 自动转 200，无需重启 api |
| 聊天/写作/翻译 502 `upstream_llm_error`（REST 或 SSE error 帧） | api 日志 `request failed`/`send message failed` 的 error 链；设置页 providers/models | Key 失效、余额/配额、base URL 错、模型名不存在、代理不通 | 设置页更新 Key/模型；用 curl 直接打 provider /models 排除网络；配置读库，修好无需重启。**区分**：应用 429 是 `type=rate_limited`+Retry-After；provider 429 归成 `upstream_llm_error` |
| 429 `rate_limited` + Retry-After | 响应体（中间件当前**不打日志**）、是否同一 client key、反代 XFF 是否覆盖 | 超过 general/expensive 档；或 XFF 追加式透传导致限流键不对 | 等 N 秒；误伤就调大 `CONGORAG_RATE_LIMIT_*`——**容器部署要改 compose 的 api `environment:`（见 §2 的说明，只改 .env 不会传进容器）**，本地 dev 直接 export；计数在内存、重启清零；紧急可先设 0；同时修反代 XFF |
| 磁盘满：上传 500、文档 failed、postgres 报 `No space left on device` | 宿主机 `df -h`、`docker system df`、各容器日志 | `pgdata`/`congorag-data` 所在盘满，或镜像/日志占满 | 扩盘/迁移卷；清无用旧镜像与悬空卷（**不要 down -v**）；绝不要删 master.key |
| SSE 到一半断流（浏览器 `ERR_INCOMPLETE_CHUNKED_ENCODING`，无 done 帧），服务端日志正常 | 反代 error log（nginx `upstream timed out`）、`proxy_buffering`/`proxy_read_timeout`、服务端心跳是否 15s 一条 | 反代缓冲/超时掐了长连接；或网络中断 | 按 §4 改反代后 reload；客户端刷新后用 `GET /conversations/{id}/events?after_event_id=N` 或带 `Idempotency-Key` 重发补事件 |
| 文档一直 `queued`/`processing`，或 worker 报错 | `docker compose logs -f worker`、文档状态、`SELECT state, kind, count(*) FROM river_job GROUP BY state, kind;`（字段以本地 River 版本为准） | worker 没起/挂了 → 全 queued；PDF sidecar 不通或 URL 空 → pdf failed；模型未配/配额不足 → embedding 失败重试；单文档超 30 分钟 → 超时 | 先修根因，再重新索引：界面「重新索引」或 `POST /api/v1/documents/{id}/reindex`、`POST /api/v1/knowledge-bases/{id}/reindex`（202，真正干活的是 worker）。最后一次 attempt 失败才落 failed |
| 迁移容器非 0 退出，api/worker 不启动 | `docker compose logs migrate` | 迁移 SQL 失败/数据库不可用/版本 dirty | 先备份；数据库可用后重跑 `docker compose run --rm migrate`（幂等）；schema dirty 时用镜像里的 migrate CLI 看完 `version` 再决定 `force`（确认 schema 状态后再做） |
| 上传 413 / 400 `upload exceeds ... byte limit` | 反代 `client_max_body_size`、`CONGORAG_MAX_UPLOAD_BYTES`、错误 detail | 反代上限 < 业务上限，或文件确实超限 | 调大反代上限且 ≥ 业务上限；业务上限改动要重启 api |
| pdf 文档 failed，日志提示"没有配置解析器" | `CONGORAG_PDF_SERVICE_URL`、pdf-service 容器状态 | 没配 sidecar，或 sidecar 挂了/超时 | 起 pdf-service 并配 URL；首次构建慢，等健康后再传；超时调 `CONGORAG_PDF_TIMEOUT` |

## 10. 部署后验收清单

### 10.1 服务与探针

~~~bash
docker compose ps                     # api/worker/postgres 跑着；migrate Exited(0)
docker compose logs migrate | tail    # 两套迁移各自完成
curl -sS http://127.0.0.1:3210/healthz                    # {"status":"ok"}
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3210/readyz   # 200；503=数据库不可用
curl -sS http://127.0.0.1:3210/api/v1/knowledge-bases     # []（空库）
~~~

### 10.2 安全响应头与 CSP（v6.4.0 起）

~~~bash
curl -sSI http://127.0.0.1:3210/ | grep -Ei \
  'content-security-policy|x-frame-options|x-content-type-options|referrer-policy'
~~~

浏览器打开首页，DevTools Console 不应有 CSP violation；主题切浅色后刷新，第一帧不应闪回深色（内联主题引导脚本被 CSP 的 sha256 放行；若改过 `web/index.html` 里那段脚本，必须同步 `internal/platform/security.go` 的 hash，`go test ./internal/platform/` 有测试专门守它）。

### 10.3 功能冒烟（按评审演示顺序）

1. **BYOK**：设置 → 模型，填 base URL/API Key/chat/embedding 模型 → 保存；再打开设置页确认 Key 不回显明文。
2. **上传**：传 `1706.03762v1.pdf` → 状态 `queued→processing→ready`，page_count=15，分块带页码。
3. **问答**：问"训练用了多少块 GPU" → SSE 先到 citation（带 page），答案每句带 [n]，结束发 `grounding` 事件，点引用跳阅读器 bbox 高亮。
4. **检索调试**：`POST /api/v1/knowledge-bases/{id}/search` 返回 vectorScore/lexicalScore/score 三列。
5. **Agent**：建 Agent → 运行"帮我搜一下最新的 RAG 论文"→ 流里出现 `run_started/tool_call/tool_result/token/done`；取消另一次运行 → `POST /runs/{runId}/cancel` 返回终态。
6. **恢复**：`docker compose restart api` 后，之前 running 的 run 会被启动扫描标 `interrupted`（ADR-007 决策二），可对它发起 resume。
7. **限流（可选，做之前知道会挡自己几分钟）**：同一来源快速连打 100 次 `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3210/api/v1/knowledge-bases`，应出现 429 且带 `Retry-After`（默认 burst 60、之后按 240/min 补充，具体第几次取决于请求速度）；`docker compose restart api` 清空内存计数。

### 10.4 仓库内质量门禁（改过代码才需要；评审可直接复跑）

| 命令 | 查什么 | 需要什么 |
| --- | --- | --- |
| `make check` | go build + go vet + go test + 前端 lint | Go 1.26、pnpm |
| `pnpm --filter web test` / `pnpm --filter web build` | 前端单测 / 生产构建 | Node 22 + pnpm 11 |
| `make generate && git diff --exit-code` | 契约改了有没有重新生成（ADR-002） | oapi-codegen v2.8.0、openapi-typescript 7.13.0 |
| `make bench-gate` | 纯函数基准回归（allocs 严格、B/op +5%、ns +30% 且 >200ns） | Go；基线 `benchmarks/baseline.json` |
| `make bundle-check` | 首屏 gzip +4% / 单业务 chunk +8% / ReaderPage +5% 超预算失败 | 先 `pnpm --filter web build`；基线 `docs/perf/bundle-baseline.json` |
| `make test-contract` | 运行时行为是否符合 OpenAPI（起 api + Prism 校验） | PostgreSQL 已起且迁移完成 |
| `make test-integration` | 需要真实 PostgreSQL 的测试（testcontainers 一次性库） | Docker；首次拉镜像 |
| `make test-e2e` | Playwright 端到端（R5 接入，全量 mock API） | `pnpm --filter web exec playwright install chromium` |
| `make loadtest` | SSE 并发压测模拟（默认 mock，`--target` 可指真实 api） | Node |
| `make crash-probe` | 硬杀 api 验证恢复语义（需 api 在跑、库可用、Agent 配 chat 模型） | 见 `docs/testing.md` |

## 11. 常用命令速查

~~~bash
docker compose ps / logs -f api / logs -f worker / down
docker compose exec postgres psql -U postgres -d congorag
docker compose run --rm migrate                  # 手工跑两套迁移（幂等）
docker compose restart api                       # 改限流/日志级别后
docker images | grep congorag                    # 核对镜像 tag
node scripts/upgrade.mjs --dir . --dry-run
node scripts/rollback.mjs --dir . --yes          # 会丢迁移后的新数据
node scripts/seed-demo.mjs --api http://127.0.0.1:3210 --dir ./测试数据/pdf
~~~

## 12. 文档索引

- 启动作业：[deployments/startup/README.md](../deployments/startup/README.md)、[deployments/production/README.md](../deployments/production/README.md)
- 升级/回滚用户视角：[docs/upgrading.md](upgrading.md)；发布者流程：[docs/releasing.md](releasing.md)
- 流式协议：[docs/sse-protocol.md](sse-protocol.md)；测试约定与崩溃探针：[docs/testing.md](testing.md)
- 性能与体积基线：[docs/performance.md](performance.md)、[docs/quality-baseline.md](quality-baseline.md)
- 架构与 ADR：[docs/architecture.md](architecture.md)、[docs/adr/](adr/)
- 契约（唯一真相）：[contracts/openapi.yaml](../contracts/openapi.yaml)
