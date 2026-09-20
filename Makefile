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

.PHONY: help up down logs psql \
        migrate-up migrate-down migrate-version migrate-create \
        river-migrate-up river-migrate-down \
        generate generate-go generate-ts \
        dev dev-web dev-worker build build-web build-web-assets build-web-placeholder build-go \
        test lint tidy fmt vet check

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
	migrate -path migrations -database "$(DB_URL)" up

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
	river migrate-up --database-url "$(DB_URL)" --line main

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

test:
	go test ./...
	pnpm --filter web test

# 前端静态检查。src/components/ui/ 和 src/hooks/use-mobile.ts 在
# .oxlintrc.json 的 ignorePatterns 里——那是 shadcn 生成的代码，改了会被覆盖。
lint:
	pnpm --filter web lint

# 【这里不用 build】check 是"提交前快速自查"，不该依赖 pnpm 装没装。
# 前端产物有没有问题，由 build 负责。
check: build-go vet test lint

tidy:
	go mod tidy

fmt:
	go fmt ./...
