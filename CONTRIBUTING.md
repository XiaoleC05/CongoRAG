# 参与贡献

完整的贡献说明在 [文档站](https://oxelia51.com/docs/contributing/) 和 [site/src/content/docs/docs/contributing.md](site/src/content/docs/docs/contributing.md)。

## 提交前

```bash
make check
```

改到这些地方还要多做一步：

| 改了什么 | 还要做什么 |
| --- | --- |
| `contracts/openapi.yaml` | `make generate`，把 Go 和 TS 产物一起提交 |
| 数据库 schema | 写一对 `up`/`down` 迁移 |
| 对外行为 | 写进 `CHANGELOG.md` 的 `### 行为变更` |
| 站点 | `cd site && pnpm build` |

生成物不要手改。`apps/api/internal/api/generated.go` 和 `packages/api-client/src/schema.d.ts` 都从契约生成。

## 提交信息

用 Conventional Commits：`feat:`、`fix:`、`refactor:`、`docs:`、`test:`、`chore:`。

## 许可证

提交 PR 即表示你同意以 MIT 许可证分发你的贡献。不需要签 CLA。
