# 发布流程

## 版本号怎么定

五处用**同一个数**（[ADR-002](adr/002-contract-versioning.md)）：

```text
contracts/openapi.yaml 的 info.version
产品版本（README、发布说明）
git tag（去掉 v 前缀）
GitHub 里程碑名
CHANGELOG 的小节名
```

写法以 `MAJOR.MINOR` 为主，热修才出现 `MAJOR.MINOR.PATCH`。历史两次
（`v1.0`、`v2.0`）都是两位，tag 名与里程碑名逐字相同。

issue 归到**将要发布它的版本**，不是发现它的版本——v1.0 的缺陷进了 v2.0
里程碑，v2.0 的那批进了 v3.0。

## 发布前

1. **写 CHANGELOG 的小节。** 它是 tag 消息与 Release 正文的**唯一来源**，
   脚本找不到对应小节会直接拒绝发布。什么必须写见 `CHANGELOG.md` 顶部的清单。
2. **把 `contracts/openapi.yaml` 的 `info.version` 改成要发布的版本号。**
   脚本会断言两者一致。这一步是有意的——没有它，契约版本会像 `0.1.0` 那样
   再次腐化（那正是 ADR-002 要消掉的状态）。
3. **该跑迁移的版本要在 CHANGELOG 里写明。** 比如 v3.0 新增了
   `0006_idempotency_replay`、`0007_agent_tool_calling` 与
   `0008_list_pagination_indexes`，漏跑 0006 的直接后果是聊天整条路径 500。
4. 跑一遍门禁：`make check`（Go 的 build/vet/test + 前端 lint/test），
   以及 `make test-integration`（需要真库，见 `docs/testing.md`）。
5. 工作区干净、在 `main` 上、与 `origin/main` 一致——脚本会检查这三条。

## 发布

```bash
make release-dry VERSION=3.0   # 先看一眼：只打印正文与将执行的命令，不碰 git、不联网
make release VERSION=3.0       # 真的发布
```

脚本按顺序做三件事：

1. `git tag -a v3.0 -F <临时文件>`——**tag 消息就是 CHANGELOG 那一节**。
   用 `-F` 传文件而不是 `-m "<正文>"`：正文几千字，Windows 上有 argv 长度与
   换行转义的风险。
2. `git push origin refs/tags/v3.0`——推具体 ref，不要 `--tags`。
3. `POST /releases`；如果这个 tag 已经有 Release 就改成 `PATCH`。

**第 3 步是幂等的，这一点是刻意的。** 脚本先推 tag 再调 API，而 API 可能失败
（网络、权限、限流）——那时留下的是「有 tag 没 Release」的状态，正是 v1.0 的
现状。幂等让重跑能补齐，而不是要求人去手工收拾。

## 凭据

脚本按 `GITHUB_TOKEN` → `GH_TOKEN` → 仓库根 `.env` 的顺序找 token。

需要一个 **fine-grained PAT**：权限 `Contents: read and write`（建 Release 用），
作用域只勾这个仓库。注意 git remote 走 SSH，所以**推 tag 用的是 SSH key、
建 Release 用的是 PAT**，两个凭据来源不同。

**任何路径下都不会打印 token**，连长度都不打；错误信息里只提变量名。

## 补建历史版本的 Release

某次发布只推了 tag、没建 Release（v1.0 就是这样），可以事后补：

```bash
make release-dry VERSION=1.0   # 先确认正文对不对
node scripts/release.mjs 1.0 --allow-existing-tag
```

`--allow-existing-tag` 是必需的——正常情况下 tag 已存在时脚本会拒绝执行，
防止误覆盖。补建时要注意：正文里如果标着「依 tag/提交/README 重建」，
那必须是真的（v1.0 的正文就是这样，因为它当时没有 Release 对象）。

## 发布后核对四件事

1. **Release 正文 == CHANGELOG 小节 + compare 链接**（脚本自动拼）。
2. **tag 指向的 commit 就是 `main` 顶端**：`git rev-parse 'v3.0^{commit}' main` 两行相同。
   **`^{commit}` 不能省**——两个 tag 都是 annotated，`git rev-parse v3.0` 给的
   是 tag 对象自己的 sha，不是它指向的 commit，不加会得到两个不同的哈希。
3. **CI 四个 job 全绿。** 注意 tag push **不触发** CI（工作流的 `on` 只有
   `push[main]` / `pull_request` / `workflow_dispatch`），所以这一条要去看
   `main` 那次 push 的运行结果。
4. **契约 `info.version` == 版本号。**

## 这个流程有意不做的事

- **不自动生成 CHANGELOG。** 从提交信息里凑出来的更新日志读起来像流水账，
  而读者要的是「升级之后我会遇到什么」。
- **不自动 bump 契约版本。** 脚本只断言，不改文件——发布前那一次修改是一个
  需要人看一眼的动作，而不是一次静默的字符串替换。
