---
title: 测试
description: 本地跑哪些测试、集成测试怎么起数据库、CI 查什么。
---

## 常用命令

```bash
make test                # go test ./... + 前端单测
make check               # build-go + vet + test + lint
make test-integration    # 起一次性 PostgreSQL 容器，跑集成测试
```

`make test` 里大部分测试不连数据库。需要真实 PostgreSQL 的测试由 `CONGORAG_TEST_DB_URL` 门控；没设变量就跳过。

所以本地 `make test` 全绿，不代表那些 SQL 跑得起来。要覆盖它们，跑 `make test-integration`。

## 集成测试

`make test-integration` 会设 `CONGORAG_TESTCONTAINERS=1`，由 `internal/testdb` 起一个一次性 PostgreSQL 容器，灌好两套迁移，再带变量跑全部测试。

这个命令会动 schema，但动的是那个一次性容器。开发库和 `make up` 起的 `congorag-postgres` 不会被碰。

第一次跑要拉镜像，会慢一些。

## 竞态检测

`GO_TEST_FLAGS=-race` 可以开竞态检测，但本机没有 gcc 时跑不起来。CI 的 ubuntu runner 有。

## 契约测试

`make test-contract` 用 Prism 起一个代理，逐条校验请求和响应是否符合 OpenAPI。

`make lint-spec` 用 spectral 检查契约本身。规则和豁免理由在仓库的 `docs/contract-lint.md`。

## CI

CI 有六个互相独立的 job：

1. Go 编译与单元测试（带 `-race`）
2. 契约生成物是否最新
3. 契约本身的 spectral lint
4. 前端构建与 go:embed
5. 真实 PostgreSQL 上的集成测试
6. 交付形态的容器装配

任何一个红了都不代表另外五个有问题；它们是六件不相干的事。

## 写测试时注意

- 不要依赖别的测试留下的 schema 状态。需要特定向量维度时，自己设、自己恢复。
- 集成测试可以改 schema，但只能改一次性容器的。
- 改契约先改 `contracts/openapi.yaml`，再 `make generate`，产物一起提交。
