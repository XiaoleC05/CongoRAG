# ADR-002：契约版本跟产品版本走

- 状态：已采纳
- 日期：2026-09-21
- 相关：`contracts/openapi.yaml`、`docs/releasing.md`、`CHANGELOG.md`

## 背景

`contracts/openapi.yaml` 的 `info.version` 从 v1.0 起就没动过，一直是
`0.1.0`，而产品已经打过 `v1.0`、`v2.0` 两个 tag。四个已验证的事实决定了
这个字段该怎么处理：

- **它没进任何生成物。** `apps/api/internal/api/generated.go` 与
  `packages/api-client/src/schema.d.ts` 里都搜不到它的值——
  `oapi-codegen.yaml` 没有开 `embedded-spec`。所以改它产生**零 diff**，
  不会触发 CI 里那条 `git diff --exit-code` 的生成物检查。
- **它不被任何代码读。** 全仓唯一打开这份契约的
  `apps/api/internal/api/contract_test.go` 读的是响应状态码，不看版本。
- **CI 不管它。** 现有的检查只 gates「改了契约但忘了重新生成」，
  而 tag push 根本不触发 CI（工作流的 `on` 只有 `push[main]` /
  `pull_request` / `workflow_dispatch`）。
- **但它确实在变。** v2.0 动过契约（新增 `Conflict` 响应与三个端点的 404
  声明），所以这不是一个「从来不变所以不用管」的字段。

结论是：`0.1.0` 既不像「未定」也不像「已稳定」，v2.0 之后尤其误导——读者
无法判断当前契约的稳定性。

## 决策

**契约版本 = 产品版本。** 五处用同一个数：

```text
contracts/openapi.yaml 的 info.version
产品版本（README 与发布说明里写的）
git tag（去掉 v 前缀）
GitHub 里程碑名
CHANGELOG 的小节名
```

版本号写法以 `MAJOR.MINOR` 为主，热修才出现 `MAJOR.MINOR.PATCH`——
历史两次（`v1.0`、`v2.0`）都是两位，tag 名与里程碑名逐字相同。

**执行机制只能放在发布脚本里**（`scripts/release.mjs`）：发布前断言
`info.version` 等于要发布的版本号，不一致就拒绝发布。CI 做不到这件事——
它在 push-to-main 上没有 tag 上下文。

## 理由

1. **A 的代价在本项目近乎为零。** 实测改 `info.version` 在生成物上是零
   diff，所以「每次发布要动契约」实际上只是 CHANGELOG 旁边改一行 YAML。
2. **B 需要纪律，而纪律已经被证明不成立。** `0.1.0` 从 v1.0 起没动过，
   就是「需要有人记得维护」这条路的实证结果。
3. **A 的执行机制恰好落在本来就有的东西上。** 发布脚本是 #52 已经要写的，
   加一条断言是顺手白得；而 B 在 CI 里只能做一条语义上不可执行的弱提醒
   （「契约改了但版本没动」既可能是漏更新、也可能是纯注释修改）。
4. **独立客户端判断兼容性这个需求在本项目不成立。** 唯一的客户端
   `web/` 与 `apps/api` 永远同一个 commit 发布，契约的兼容边界本来就由
   路径里的 `/api/v1` 表达。

## 后果

- **好处**：任何一个版本号都能在三处互相印证；发布脚本的断言让「忘了改
  契约版本」变成一次失败的发布，而不是一次静默的腐化。
- **代价**：补丁级发布也会动契约版本，「契约没变」与「产品没变」被绑在
  一起。对只有一个客户端的项目，这个代价可以接受。
- **必须一起记住的**：**兼容性承诺的边界是路径里的 `/api/v1`，不是这个
  版本号。** 读者不要把 `info.version` 读成稳定性保证——它标记的是
  「这份契约属于哪一个产品版本」。
- **如果将来要重新考虑**：出现独立的第三方 API 消费者时改回选项 B
  （契约有自己的语义化版本，只在破坏性变更时升 major），并在 CI 里加一条
  「契约的 paths/components 变了但版本没动就警告」的检查。注意那条检查有
  一个真陷阱：GitHub 的 run 用 `bash -eo pipefail`，
  `git diff ... | grep -q ...` 会因 grep 提前退出触发 SIGPIPE 让整条管道
  失败——必须先把 diff 落到临时文件再 grep。
