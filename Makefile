# ConGoRAG 开发命令入口
#
# 【这个 Makefile 必须同时能在 cmd.exe 和 bash 里跑】
# Windows 上 make 是原生版，它找不到 /bin/bash 就会退回 cmd.exe。
# 所以下面【不要用】任何 shell 特有的语法：
#   ✗  VAR=值 命令          （bash 的前缀赋值，cmd 不认）
#   ✗  $(...)  命令替换      （bash 语法，cmd 里是 %VAR%）
#   ✗  grep / sed / printf   （cmd 里没有）
# 需要传环境变量用 make 自己的 `export`——它由 make 设置，跟 shell 无关。

# golang-migrate 和 oapi-codegen 都由 `go install` 装进 GOPATH/bin，
# 而这个目录在 Windows 上默认不在 PATH 里。这里自己拼出来。
# $(subst \,/, ...) 把 Windows 的反斜杠转成正斜杠（\, 是转义的逗号分隔符）。
GOPATH_BIN := $(subst \,/,$(shell go env GOPATH))/bin

# 【PATH 的分隔符随平台变，不能写死】Windows 用 `;`，POSIX 用 `:`。
# 写死 `;` 的话在 Linux 上第一个条目会变成 "<gopath>/bin;/usr/local/bin"——
# 一个不存在的目录，后面所有条目也跟着错位，make 里调 migrate/oapi-codegen
# 就是 "command not found"（CI 因此绕开了 make，见 .github/workflows/ci.yml）。
# $(OS) 是 make 在 Windows 上自己设的，cmd.exe 和 bash 里都是 Windows_NT，
# 所以在 Windows 上拿到的分隔符和以前完全一样。
ifeq ($(OS),Windows_NT)
PATH_SEP := ;
else
PATH_SEP := :
endif
export PATH := $(GOPATH_BIN)$(PATH_SEP)$(PATH)

COMPOSE := docker compose -f deployments/docker/docker-compose.yml
PG_CONTAINER := congorag-postgres
DB_URL := postgres://postgres:postgres@127.0.0.1:5432/congorag?sslmode=disable

# 【用 make 的 export 传环境变量，不用 shell 的前缀赋值】
# 这样 cmd 和 bash 都能工作——make 在启动命令之前把变量放进环境里。
export CONGORAG_DB_URL := $(DB_URL)

# 两条迁移命令在这里写一份，migrate-up / river-migrate-up 引用它们。
# := 是立刻展开，不调 shell。
MIGRATE_UP_CMD := migrate -path migrations -database "$(DB_URL)" up
RIVER_MIGRATE_UP_CMD := river migrate-up --database-url "$(DB_URL)" --line main

# 【就绪判据必须按容器名，不能用 compose 派生写法】docker compose ps /
# compose port 要求容器带 compose 标签；手工用 Docker Desktop 起的同名容器
# 没有那些标签，用它们会误报"没在跑"，而用户会以为是 Docker 坏了。
PG_RUNNING = $(shell docker inspect -f "{{.State.Running}}" $(PG_CONTAINER) 2>&1)
GO_TEST_FLAGS ?=

# 【spectral 的版本锁在这里，别处不要再写一遍】
# npx 的 `pkg@版本` 写法会按版本缓存到 npm 的 _npx 目录，第二次跑不重新下载。
# 【为什么用 npx 而不是全局安装】这个项目靠的是"本机有 node"这一个前提
# （见 ci.yml 顶部那条"工具版本全部锁死"的理由），全局安装会多出一个
# "先跑 npm i -g"的隐性前置步骤，而它和 go install 那两个工具不一样——
# 那两个必须全局是因为 Makefile 直接调它们，spectral 只在这一条 target 里出现。
#
# 【为什么加 --no-fund/--no-audit】npx 首次下载会把 npm 的赞助与漏洞提示
# 混进 lint 输出里；这一条 target 的输出要能直接被读，噪声就是失败信息。
SPECTRAL := npx --yes --no-fund --no-audit @stoplight/spectral-cli@6.15.0

.PHONY: help up down logs psql \
        migrate-up migrate-down migrate-version migrate-create \
        river-migrate-up river-migrate-down \
        generate generate-go generate-ts \
        dev dev-web dev-worker build build-web build-web-assets build-web-placeholder build-go \
        test test-integration test-contract lint tidy fmt vet check         release release-dry check-changelog \
        lint-spec crash-probe startup-package upgrade rollback

help:
	@echo ConGoRAG 开发命令
	@echo   make up                 起 PostgreSQL
	@echo   make down               停 PostgreSQL（不加 -v，数据保留）
	@echo   make logs               看 PostgreSQL 日志
	@echo   make psql               连上数据库
	@echo   make migrate-up         跑所有未执行的迁移（业务表，golang-migrate）
	@echo   make migrate-down       回退一步
	@echo   make migrate-version    看当前跑到第几号了
	@echo   make migrate-create NAME=xxx   新建一对迁移文件
	@echo   make river-migrate-up   跑 River 自带的迁移（队列表，和上面那套并存）
	@echo   make generate           从契约生成 Go 代码
	@echo   make dev                跑 api（:3210）
	@echo   make dev-web            跑 Vite 开发服务器（:5173，改前端要用这个）
	@echo   make dev-worker         跑 worker（文档处理的消费端）
	@echo   make test-integration   跑集成测试（自己起一个 PostgreSQL 容器，不动开发库）
	@echo   make test-contract      Prism proxy 契约测试（起 api，逐条校验请求与响应）
	@echo   make lint-spec          spectral lint 契约（规则集见 .spectral.yaml）
	@echo   make crash-probe        在 run 执行到一半时硬杀 api，看崩溃后的库状态
	@echo   make release VERSION=3.0 发布：打 tag + 建 Release（正文来自 CHANGELOG）
	@echo   make release-dry VERSION=3.0  只打印发布内容，不碰 git、不联网
	@echo   make check              build + vet + test
	@echo   make fmt                格式化
	@echo   开发前端要开两个终端：一个 make dev，一个 make dev-web，浏览器开 :5173
	@echo   处理文档上传还要第三个终端：make dev-worker

## ── 依赖服务 ───────────────────────────────────────────

up:
	$(COMPOSE) up -d --wait

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f postgres

psql:
	docker exec -it $(PG_CONTAINER) psql -U postgres -d congorag

## ── 数据库迁移 ─────────────────────────────────────────

migrate-up:
	$(MIGRATE_UP_CMD)

# 【注意】裸 `down` 是回退全部，必须写 `down 1` 才是回退一步。
migrate-down:
	migrate -path migrations -database "$(DB_URL)" down 1

migrate-version:
	migrate -path migrations -database "$(DB_URL)" version

migrate-create:
	migrate create -ext sql -dir migrations -seq $(NAME)

# 【两套迁移系统并存，不是笔误】River 自带一套迁移（管它自己的队列表：
# river_job / river_leader / river_client 等），和上面 golang-migrate 管的
# 业务表（knowledge_bases / documents / ...）完全独立，谁也不知道对方存在。
# 技术方案 §二明确写了"两套并存"——省下 river 那几张表自己手写迁移的功夫，
# 代价是要分别跑两次迁移命令，这里各自留一个 target。
river-migrate-up:
	$(RIVER_MIGRATE_UP_CMD)

river-migrate-down:
	river migrate-down --database-url "$(DB_URL)" --line main --max-steps 1

## ── 代码生成 ───────────────────────────────────────────

# 【产物必须提交进 git】CI 会跑 generate + git diff --exit-code，
# 检查"改了契约但忘了重新生成"。
generate: generate-go generate-ts

generate-go:
	oapi-codegen -config oapi-codegen.yaml contracts/openapi.yaml

# TS 那一半：openapi-typescript 把同一份契约生成 packages/api-client/src/schema.d.ts
generate-ts:
	pnpm --filter @congorag/api-client generate

## ── 应用 ───────────────────────────────────────────────

# 数据库要先用 make up 起。
# CONGORAG_DB_URL 由上面的 export 注入，不需要在这里写。
#
# 【这个 target 只起后端】它不构建、也不监听前端源码。
# :3210 发出去的前端是 go:embed 进二进制的【构建产物】——改了 web/src/ 之后
# 不重新 make build-web，:3210 上什么都不会变，而且不报错。
# 改前端一律另开一个终端跑 make dev-web，浏览器开 :5173。
dev:
	go run ./apps/api

# Vite 开发服务器（:5173，有 HMR）。
# 它把 /api 和 /healthz 代理到 :3210（见 web/vite.config.ts 的 server.proxy），
# 所以浏览器全程只看到一个源，不触发跨域。
dev-web:
	pnpm --filter web dev

# worker 进程：消费文档处理任务（parse → chunk → embed → index）+
# 孤儿文件对账周期任务。它不提供 HTTP，纯粹是 River 的消费端，
# 起不起它不影响 api 进程本身能不能跑——只影响"上传的文档会不会被处理"。
dev-worker:
	go run ./apps/worker

## ── 质量 ───────────────────────────────────────────────

# 【顺序不能反】go:embed 要读 apps/api/web/，那是前端构建的产物。
# 先跑 go build 的话那个目录里只有占位文件，或者干脆不存在——编译失败。
build: build-web build-go

# build-web 拆成两步，顺序不能反：先跑 vite，再把占位文件补回来。
#
# 【为什么必须补】vite 的 emptyOutDir 会清空 outDir，而 outDir 就是 go:embed
# 要读的 apps/api/web/（web/vite.config.ts）。那个目录里唯一被 git 跟踪的
# 文件是 .gitkeep，而 vite 的 skip 列表只跳过字面量 .git，所以每次构建都会
# 把它删掉——删掉之后 git 里那个路径就不存在了，新 clone 里
# `go:embed all:web` 会因为 "no matching files found" 编译失败
#（.github/workflows/ci.yml 的 go job 就是拦这个的）。
#
# 【为什么拆成两个 target 而不是写在同一条 recipe 里】make 在跑第一条命令
# 之前就把整段 recipe 展开完，所以 $(file ...) 写成 vite 后面的一行没用——
# 它在 vite 启动前就已经执行、写出来的文件随即被 emptyOutDir 擦掉（实测）。
#
# 【为什么用 $(file ...) 而不是 touch / echo】这个 Makefile 要同时跑在
# cmd.exe 和 bash 里，而两者没有一个共同的"创建空文件"命令。$(file ...)
# 是 make 自己的函数，不经过 shell——正好符合文件头那条规则。内容是什么
# 无所谓（make 会补一个换行），这个文件的作用只是让目录在 git 里活下来。
#
# 【web/package.json 的 build 脚本里也补了一次，不是重复】直接跑
# `pnpm --filter web build`（CI 的 web job 就是这么跑的）不会经过这个
# Makefile，占位文件照样会被 emptyOutDir 擦掉。两处写的都是平台换行符
# （make 补 \n/\r\n，node 写 os.EOL），和 git 检出的那份内容一致，所以
# 两种入口跑完工作区都是干净的。
build-web: build-web-assets build-web-placeholder

# 【这两条依赖声明不是多余的】上面那行把两个 target 并列为 build-web 的前提，
# 单纯的并列不保证先后——串行 make 恰好按从左到右跑，所以 `make build-web` 是
# 对的；但 `make -j` 下 build-web-placeholder 可能先跑，占位文件刚写出来就被
# vite 的 emptyOutDir 擦掉，#28 那个缺陷就静默回来了。这里把顺序写成真正的依赖。
build-web-placeholder: build-web-assets

build-web-assets:
	pnpm --filter web build

build-web-placeholder:
	$(file >apps/api/web/.gitkeep,)

build-go:
	go build ./...

vet:
	go vet ./...

# ── 发布 ────────────────────────────────────────────────────
#
# 【脚本必须是 .mjs 且不依赖 gh】本机没有装 gh；仓库根也没有 package.json，
# .js 会被当成 CommonJS 而顶层 await 不可用。见 scripts/release.mjs 的注释。
#
# 缺 VERSION 用 $(error) 报错——它在 recipe 执行到那一行时才炸，不会让
# make help 也失败。

release:
	$(if $(VERSION),,$(error 用法：make release VERSION=3.0))
	node scripts/release.mjs $(VERSION)

release-dry:
	$(if $(VERSION),,$(error 用法：make release-dry VERSION=3.0))
	node scripts/release.mjs $(VERSION) --dry-run

check-changelog:
	node scripts/release.mjs --check-changelog

test:
	go test ./...
	pnpm --filter web test

# ── 集成测试（需要真实 PostgreSQL）────────────────────────────
#
# 【测试库从哪来：现在由测试自己起（issue #70）】
# 这条 target 设 CONGORAG_TESTCONTAINERS=1，于是 internal/testdb 会自己起一个
# 容器（镜像与开发期同一个）、灌好两套迁移，跑完自动销毁。
#
# 在此之前它要求开发机上那个 compose Postgres 在跑，并手工重建一个独立测试库、
# 手工跑两套迁移——四步外部状态，而 CI 用的是另一套（workflow 的 service
# container）。现在两条路径都由 internal/testdb 保证「返回时 schema 已就绪」，
# 差别只剩库从哪来，而那个差别写在代码里（见那个包的注释）。
#
# 【为什么不再需要"独立测试库"】容器是一次性的，internal/llm 那几条会 ALTER
# 向量列类型的测试怎么改坏它都不影响任何人——而"不碰开发库"正是当初要一个
# 独立库的全部理由。
#
# 【要在本机也开竞态检测就加 GO_TEST_FLAGS=-race】
# 本机没有 gcc 时 -race 跑不起来（需要 cgo），CI 的 ubuntu runner 上可以。

test-integration: export CONGORAG_TESTCONTAINERS := 1
test-integration:
	@echo 集成测试：internal/testdb 会自己起一个 PostgreSQL 容器
	@echo （镜像与开发期同一个），灌好两套迁移，跑完自动销毁。
	@echo 你的开发库与开发期的容器都不会被碰到。
	@echo 第一次跑要拉镜像，会慢一些。
	go test $(GO_TEST_FLAGS) ./...

# ── 契约测试（issue #69）────────────────────────────────────────
#
# 【它和 lint-spec / contract job 查的不是一回事】
#   · `make generate + git diff --exit-code` → 改了契约有没有重新生成；
#   · `make lint-spec`                       → 这份契约本身写的对不对；
#   · 这一条                                  → **运行时行为**符不符合契约。
# 第三条是前两条都答不了的：handler 漏一个字段、状态码写成 200 而不是 204、
# 错误体形状走偏，生成器和 linter 一个都拦不住。
#
# 【为什么逻辑全在脚本里，这里只是一层壳】它要编译并起一个 api、起一个
# prism proxy、发一串请求、再扫 prism 的日志。这些在 cmd.exe 和 bash 里没有
# 一份共同写法，而 Makefile 的头号规则是"两个 shell 都得能跑"（见文件头）。
#
# 【它需要 PostgreSQL 在跑、且迁移已应用】本机就是 `make up` + 两套迁移那套；
# CI 里用 integration job 的 service container（见 ci.yml）。
# 端口故意避开 3210/4010，能在 `make dev` 开着的时候同时跑。
test-contract:
	node scripts/contract-test.mjs $(CONTRACT_ARGS)

# 前端静态检查。src/components/ui/ 和 src/hooks/use-mobile.ts 在
# .oxlintrc.json 的 ignorePatterns 里——那是 shadcn 生成的代码，改了会被覆盖。
lint:
	pnpm --filter web lint

# 契约 lint（issue #68）。规则集与豁免理由全在 .spectral.yaml，不在这一行里。
#
# 【它和 make check 里那些检查查的不是一回事】
#   · contract job 的 `make generate + git diff --exit-code` 回答
#     "改了契约有没有重新生成"；
#   · 这一条回答"**这份契约本身**写的对不对"。
# 前者对一份写错但生成物同步的契约完全无感。
#
# 【--fail-severity=warn 不能省】oas 规则集里绝大多数检查的严重度是 warning，
# 而 spectral 默认只在 error 上返回非零。不显式提这一档，这个 target 会对着
# 所有真正有价值的问题保持绿色。
#
# 【为什么不进 make check】check 是"提交前快速自查"，承诺的是不联网、不依赖
# 外部服务（见下面 check 的注释）；spectral 是 npx 拉下来的，首次跑必须联网。
# 契约 lint 只在 CI 的 spec job 和"改了 contracts/openapi.yaml 之后"需要跑。
lint-spec:
	$(SPECTRAL) lint contracts/openapi.yaml --ruleset .spectral.yaml --fail-severity=warn

# 【这里不用 build】check 是"提交前快速自查"，不该依赖 pnpm 装没装。
# 前端产物有没有问题，由 build 负责。
check: build-go vet test lint

tidy:
	go mod tidy

fmt:
	go fmt ./...

# ── 崩溃探针（issue #62）────────────────────────────────────────
#
# 【它解决的问题】优雅退出的路径（拒新请求 → 排空 SSE → worker 留收尾时间）
# 会把状态收拾干净，恰好绕开 resume 要处理的所有情况。只有一个不打招呼的
# KILL 才能制造出「第 N 步跑到一半、进程没了」的真实现场。
#
# 【为什么不用 Ctrl-C】`docker compose up -d` 下 Ctrl-C 碰不到容器——
# 它只影响发起它的那个终端；就算 api 跑在前台，Ctrl-C 走的是 SIGINT，
# 那条路径会把它该写的终态写完再退。两条都不产生"停在 running 的行"。
#
# 【为什么这一条 target 只是一层壳】判据（轮询库、选时机、比对前后状态）
# 在 scripts/crash-probe.mjs 里；Makefile 只负责转参数。理由是这个脚本要
# 跑 SQL、要读进程表、要开 SSE 连接——这些东西在 cmd.exe 和 bash 里没有
# 一份共同写法，而 Makefile 的头号规则是"两个 shell 都得能跑"。
#
# PROBE_ARGS 的用法见 node scripts/crash-probe.mjs --help。
crash-probe:
	node scripts/crash-probe.mjs $(PROBE_ARGS)

# ── 交付 / 运维（issue #73 / #74）───────────────────────────────
#
# 【这三条操作的是"启动包那套环境"，不是开发期那套】
# 开发期只有 PostgreSQL 在 Docker 里（deployments/docker/docker-compose.yml）；
# 下面三条走的是 deployments/startup/docker-compose.yml——api / worker / postgres
# 三个都在容器里，用镜像分发，不要求本机有 Go 工具链。
#
# 【startup-package 需要本机 Docker + node】它要 build 镜像、docker save 出
# tarball、再按平台挑一份打进 zip。CI 里由 .github/workflows/release.yml 调用
# 同一个脚本（那边镜像已经由 matrix 构建好，只是装配）。
#
# 【产物落在 dist/，而 dist/ 不在 .gitignore 里】跑完记得别把它提交上去
# （一个包 40 MB 起）。要长期跑本地打包的话，把 dist/ 加进 .gitignore。
startup-package:
	node scripts/startup-package.mjs --out dist $(PACKAGE_ARGS)

# 升级：备份 → 备份校验通过 → 迁移 → 换镜像。任一步失败即中止，见脚本注释。
# 默认升到 .env 里 CONGORAG_VERSION 指定的版本。
#
# 【--dir 默认指到 deployments/startup】那是启动包那套 compose 所在的地方；
# 脚本要读同目录的 .env（含 CONGORAG_VERSION 与 CONGORAG_DB_URL）。
# 不写这个默认值的话，从仓库根跑会去找根目录的 docker-compose.yml——那里没有，
# 报出来的是"找不到 compose 文件"，而真实原因是目录不对。
upgrade:
	node scripts/upgrade.mjs --dir deployments/startup $(UPGRADE_ARGS)

# 回滚：还原 dump + 回退镜像 tag。默认取 deployments/startup/backups 下最新的一份。
rollback:
	node scripts/rollback.mjs --dir deployments/startup $(ROLLBACK_ARGS)
