# ADR-003：两套迁移系统并存

- 状态：已采纳
- 日期：2026-09-21
- 相关：`Makefile`、`.github/workflows/ci.yml`、`docs/adr/001-modular-monolith.md`

## 背景

仓库里有**两套互不知晓的迁移系统**，各自管一批表：

| 系统 | 管什么 | 怎么跑 |
| --- | --- | --- |
| `golang-migrate` | 业务表：`knowledge_bases` / `documents` / `document_chunks` / `conversations` / `messages` / `memories` / `llm_providers` / `llm_models` / `agents` … | `make migrate-up` |
| River 自带 | 它自己的队列表：`river_job` / `river_leader` / `river_client` / `river_queue` … | `make river-migrate-up` |

它们在同一个数据库里，两套版本号（`schema_migrations` 与 `river_migration`
两张表），谁也不知道对方存在。新人第一次看 `Makefile` 会以为那是笔误。

## 决策

**保持两套并存，不为统一而把 River 的队列表改成手写迁移。**

## 理由

1. **River 的队列表 schema 是它自己的实现细节。** 手写迁移意味着每次升级
   River 都要读它的 changelog、比对表结构、自己补 DDL——而那正是上游已经
   提供 `river migrate-up` 要做的事。
2. **代价是可见且局部的**：两条迁移命令、两套版本号、两套 dirty 状态。
   这个代价落在**运维流程**上，而统一的收益只是「少一条命令」。
3. **官方支持这个用法。** River 提供 CLI 就是给「应用自己管业务表、River
   管队列表」这种集成方式用的。

## 后果

- **好处**：升级 River 不需要动任何手写迁移；两套表族的生命周期互不干扰
  （业务表回退不影响队列，反之亦然）。
- **代价**：任何一次从零搭建都要跑**两条**迁移命令，漏一条的症状是
  「任务永远停在 retryable」——不报错、不崩溃，只是不动。测试里
  `internal/platform/scheduler_integration_test.go` 就是被这条咬过之后补的。
- **必须一起记住的**：`make migrate-version` 看的是业务表那套；
  River 的版本要单独查 `river_migration` 表。
- **如果将来要重新考虑**：如果 River 的队列表结构在某次升级里变得需要
  应用层参与（比如要加自定义索引才能撑住量），那时再讨论把队列表纳入业务
  迁移。触发条件是「River 的迁移不再能独立完成它自己的表」，
  而不是「两条命令有点烦」。
