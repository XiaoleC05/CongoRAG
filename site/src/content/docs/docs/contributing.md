---
title: 贡献与反馈
description: 怎么报问题、提建议、改代码。
---

## 报问题

用 GitHub Issues：<https://github.com/XiaoleC05/CongoRAG/issues>

一个能复现的问题，最好带上：

- 你跑的版本或 commit
- 操作系统、Go/Node/Docker 版本
- 复现步骤
- 期望结果和实际结果
- 相关日志（去掉 Key、连接串、文档内容）

不要贴 API Key、数据库密码、主密钥或完整的私人文档。

## 提建议

功能建议也走 Issues。说清楚使用场景，不要只写“希望支持 X”。CongoRAG 的边界是本地优先、无账号、单实例；和这个边界冲突的建议，大概率不会做。

## 改代码

```bash
git clone git@github.com:XiaoleC05/CongoRAG.git
cd CongoRAG
git checkout -b fix/xxx
```

提交前至少跑：

```bash
make check
```

改到这些地方还要多做一步：

| 改了什么 | 还要做什么 |
| --- | --- |
| `contracts/openapi.yaml` | `make generate`，把 Go 和 TS 产物一起提交 |
| 数据库 schema | 写一对 `up`/`down` 迁移 |
| 对外行为（端点、状态码、默认值、环境变量） | 写进 `CHANGELOG.md` 的 `### 行为变更` |
| 前端 | 读 `web/README.md`，改完跑前端测试 |

生成物不要手改：`apps/api/internal/api/generated.go` 和 `packages/api-client/src/schema.d.ts` 都从契约生成，CI 会检查有没有忘记重新生成。

## 提交信息

用 Conventional Commits：`feat:`、`fix:`、`refactor:`、`docs:`、`test:`、`chore:`。

## 没有 CLA

不需要签贡献者许可协议。提交 PR 即表示你同意以 MIT 许可证分发你的贡献。
