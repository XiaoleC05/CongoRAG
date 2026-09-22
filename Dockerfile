# ConGoRAG 交付期镜像（issue #73）
#
# 【这个 Dockerfile 属于"交付形态"，不是开发形态】
# 开发期只有 PostgreSQL 跑在容器里（deployments/docker/docker-compose.yml），
# api / worker / 前端都在宿主机上跑——改一行代码重建一次镜像的话，反馈从
# 1 秒变 30 秒。这个文件服务的是另一件事：让"只想用"的人不必先装 Go 工具链。
#
# 用法（四个 target 各自是一个可用的镜像）：
#   docker build --target api     -t congorag-api:3.0 .
#   docker build --target worker  -t congorag-worker:3.0 .
#   docker build --target migrate -t congorag-migrate:3.0 .
#   （startup-package 那个 target 是给打包脚本用的，产出一个带三个镜像的目录）
#
# 一般不用手敲上面三条——`node scripts/startup-package.mjs` 会按平台构建、
# 打 tag、docker save 成 tarball。手敲的场合是调试。
#
# 【为什么四个 target 而不是四个 Dockerfile】它们共用前三层（前端构建、
# Go 编译、CLI 工具），拆开就要把这套构建跑三遍。多阶段构建在这里不是
# "复用"的修辞，是省掉两次 5 分钟的 vite + go build。

# ── 1. 前端产物 ─────────────────────────────────────────────────
#
# 【这一层不能省，也不能挪到后面】apps/api/main.go 有
# `//go:embed all:web`，api 的界面是**编译期**嵌进二进制的。少了这一层，
# Go 那边要么编译失败（目录不存在），要么更糟——编译成功但界面是空的：
# apps/api/web/ 里有 git 跟踪的 .gitkeep，而 `all:` 前缀会把点开头的文件
# 也匹配上，所以 go:embed 认为"有文件"，MountSPA 启动时只打一条警告。
# 下面那句 test -f 就是为这个"更糟"准备的。
FROM node:22-bookworm-slim AS web

# pnpm 的版本跟 ci.yml 的 pnpm/action-setup 一致（11）。corepack 是 node
# 官方镜像自带的，不用额外装。
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate

WORKDIR /src

# 【先只 COPY 依赖清单再 install】这样改一行 web/src 不会让 pnpm install
# 那一层缓存失效。四次 COPY 是因为 pnpm workspace 的成员各自有一份
# package.json，少一份 install 就会以"找不到 workspace 包"失败。
COPY pnpm-workspace.yaml pnpm-lock.yaml ./
COPY web/package.json ./web/
COPY packages/api-client/package.json ./packages/api-client/

RUN pnpm install --frozen-lockfile

# 再拷源码。contracts/ 也要——packages/api-client 的 generate 脚本引用它，
# 虽然生成的 src/schema.d.ts 已经提交进 git、构建时不需要重跑 generate，
# 但把 contracts/ 带上能让"镜像里能不能重新生成"这个问题的答案是肯定的。
COPY contracts ./contracts
COPY packages ./packages
COPY web ./web
COPY apps ./apps

# 【为什么不是 `make build-web`】Makefile 那两条 target 里有 $(file ...) 和
# 平台判断，是给"本机同时跑 cmd.exe 和 bash"准备的；容器里只有一个 shell，
# 直接调 pnpm 更短也更不容易误解。代价是这里绕过了 Makefile 的
# build-web-placeholder——所以下一句必须自己把这件事补上。
RUN pnpm --filter web build

# 【这一句是整层里最值钱的一句】它拦的正是上面说的那个坑：vite 的
# emptyOutDir 把 apps/api/web/ 清空（连 .gitkeep 一起），如果构建因为
# 任何原因没产出 index.html，`go build` 依然会成功——因为 .gitkeep 被
# web/package.json 的 build 脚本补回来了，`go:embed all:web` 匹配得到它。
# 那会产出一个"能跑、能健康检查通过、打开是空白页"的 api，而错误信息
# 要等到用户访问首页才出现。宁可在构建期红一次。
RUN test -f apps/api/web/index.html \
    || (echo "前端产物不在 apps/api/web/ 里——go:embed 会嵌进一个空目录" >&2; exit 1)

# ── 2. Go 二进制 ────────────────────────────────────────────────
#
# 【CGO_ENABLED=0】交付形态要跨平台交叉编译（release.yml 的 matrix 覆盖
# 六个 GOOS/GOARCH 组合），而 cgo 交叉编译需要一个目标平台的目标工具链。
# 这个项目的依赖全是纯 Go 的（pgx、eino、tiktoken），关掉 cgo 不损失功能。
# 顺带得到一个静态链接的二进制，能塞进 alpine 这类小镜像。
FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 【覆盖，不是并存】上一句 COPY . . 带进来的是 git 里那份 apps/api/web/，
# 里面只有 .gitkeep（真实产物在 .gitignore 里）。这一句把第 1 层的真产物
# 盖上去，才是"带界面的二进制"。
COPY --from=web /src/apps/api/web ./apps/api/web

ARG TARGETOS=linux
ARG TARGETARCH=amd64

# -trimpath 去掉构建机的绝对路径，产物在不同机器上是可复现的。
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/api ./apps/api

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/worker ./apps/worker

# ── 3. 迁移 CLI ─────────────────────────────────────────────────
#
# 【为什么迁移工具要进镜像】用户在启动包那套环境里没有 Go 工具链，
# `make migrate-up` 对他们不存在。而升级时**必须**跑迁移——v3.0 的
# CHANGELOG 写明了漏跑 0006 的直接后果是聊天整条路径 500。
# 所以镜像自带这两个 CLI，compose 里有一个一次性的 migrate 服务。
#
# 【版本必须和 ci.yml 的 env 一致】v4.20.1 / v0.47.0 都是从 ci.yml 抄过来的，
# 那边锁它们的理由（生成的 schema 与生产一致）在这里同样成立，而且更强：
# 用户拿到的镜像里的迁移工具与 CI 验证过的那个必须是同一个版本。
FROM golang:1.26-bookworm AS tools

# 【CGO_ENABLED=0 在这里是必需的，不是顺手抄上面的】
# golang 基础镜像和 runtime 基础镜像的 libc 不是同一个：前者是 debian 的
# glibc，后者是 alpine 的 musl。开着 cgo 编译出来的二进制动态链接到
# /lib64/ld-linux-x86-64.so.2，那个文件在 alpine 里不存在——而**报错信息
# 是 `migrate: not found`**，看起来像"文件没拷进去"。实测踩过一次：
# `ls -l /usr/local/bin/` 里三个文件都在、都有执行位、入口脚本也是 LF，
# 但 `migrate up` 依然 exit 127。（api / worker 那两个没这个毛病，
# 因为它们在 build 层就已经 CGO_ENABLED=0 了。）
# 两个 CLI 的依赖都是纯 Go 的（migrate 的 postgres 驱动是 lib/pq），
# 关掉 cgo 不损失功能，顺带得到两个静态二进制。
ENV CGO_ENABLED=0

RUN go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1
RUN go install github.com/riverqueue/river/cmd/river@v0.47.0

# ── 4. 三个交付镜像共用的底座 ────────────────────────────────────
#
# 【为什么是 alpine 而不是 distroless】应用要往卷里写东西
#（/data/documents、master.key、tiktoken 词表缓存），而 distroless 的
# 非 root 用户配合宿主机的命名卷很容易变成"写不进去"——那类故障的表现
# 是上传文档报 500，排查要绕一大圈。alpine 带 shell，出问题时
# `docker compose exec api sh` 能进去看一眼，这个可诊断性在交付形态里
# 比省下的那 5MB 值钱。
FROM alpine:3.22 AS runtime

# ca-certificates：api 要出网访问 LLM 提供方（https）。没有它，
# Go 的 TLS 客户端会以 "x509: certificate signed by unknown authority"
# 失败——而错在配置层，看起来像 key 填错了。
# tzdata：日志与 created_at 的可读显示。
RUN apk add --no-cache ca-certificates tzdata

# 【非 root + uid 固定】uid 写成 10001 是刻意的：Docker 用镜像里该路径的
# 属主去初始化命名卷，uid 固定下来，卷的属主就是确定的。用随机的 uid 时，
# 换个基础镜像就可能让已有卷变得不可写。
RUN adduser -D -u 10001 -h /app congorag

# 【/data 是唯一有状态的地方】数据库在 postgres 容器里，原始文件、
# 主密钥、tiktoken 缓存三样在这里。三者必须待在同一个卷里：
# 主密钥丢了的话，库里那些用主密钥加密的 provider api key 全部作废。
RUN mkdir -p /data/documents /data/tiktoken-cache \
    && chown -R 10001:10001 /data

WORKDIR /app

# 【为什么要把三个默认路径显式改成 /data 下】internal/platform/config.go 的
# 默认值是 ./data/*（相对 WORKDIR），改 WORKDIR 就能达到同样效果，但显式
# 写出来能让"哪些东西是持久的"在 `docker inspect` 里一眼看到，不依赖读者
# 去推 WORKDIR 是什么。
ENV CONGORAG_DOCUMENTS_DIR=/data/documents \
    CONGORAG_MASTER_KEY_PATH=/data/master.key \
    CONGORAG_TIKTOKEN_CACHE_DIR=/data/tiktoken-cache \
    CONGORAG_PORT=3210

USER congorag

# ── 5. api ──────────────────────────────────────────────────────
FROM runtime AS api

COPY --from=build /out/api /usr/local/bin/api

# 【监听地址必须由 compose 显式给成 0.0.0.0:3210】config.go 的默认值是
# 127.0.0.1:3210（安全考虑，见那里的注释）。容器里绑回环的话，宿主机的
# 端口映射转发不进来——容器会正常启动、日志照常打印 listening，但外面
# 连不进来。这里**不**设默认值：设了就等于把那个坑藏进镜像里，而
# 漏配这件事必须在下一次 docker compose config 时就看得见。
# 见 deployments/startup/docker-compose.yml 与 deployments/startup/.env.example。
EXPOSE 3210

# 【健康检查用 wget 而不是 curl】alpine 自带 busybox wget，没有 curl；
# 为一条健康检查再装一个包不值。127.0.0.1 在容器内始终可用——容器内部
# 的分组是 0.0.0.0，回环自然也在里面。
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- http://127.0.0.1:3210/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/api"]

# ── 6. worker ───────────────────────────────────────────────────
#
# 【没有 EXPOSE、没有 HEALTHCHECK】它不提供 HTTP（apps/worker/main.go 的
# 注释写明"这个进程没有 go:embed，也不内嵌前端"）。给它挂一个端口探针
# 会得到一个永远 unhealthy 的容器——而它只要在消费 River 队列就是健康的。
# 这一条刻意的缺失写在这里，免得下一个人以为漏了。
FROM runtime AS worker

COPY --from=build /out/worker /usr/local/bin/worker

ENTRYPOINT ["/usr/local/bin/worker"]

# ── 7. migrate ──────────────────────────────────────────────────
#
# 【它是一个一次性容器，不是常驻服务】compose 里以
# `restart: "no"` + `depends_on: service_completed_successfully` 使用：
# api / worker 要等它退出码为 0 才启动。
#
# 【两套迁移都要跑】业务表由 golang-migrate 管（migrations/），River 自己的
# 队列表由 river CLI 管，谁也不知道对方存在（见 Makefile 的注释）。
# 少跑一套的表现是 worker 起来后一直报 relation "river_job" does not exist。
FROM alpine:3.22 AS migrate

RUN apk add --no-cache ca-certificates tzdata

COPY --from=tools /go/bin/migrate /usr/local/bin/migrate
COPY --from=tools /go/bin/river /usr/local/bin/river
COPY --from=build /src/migrations /migrations

COPY deployments/docker/migrate-entrypoint.sh /usr/local/bin/migrate-entrypoint
RUN chmod +x /usr/local/bin/migrate-entrypoint

ENTRYPOINT ["/usr/local/bin/migrate-entrypoint"]
