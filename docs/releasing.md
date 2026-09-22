# 发布流程

> 这份文档是给**发布者**（维护者）的：怎么把一个版本发出去。
> 给**用户**的是 [`upgrading.md`](upgrading.md)——怎么把一个已经发出去的版本
> 装起来、升级、回滚。两边混在一起写过一次，结果是发布者以为用户会自己跑
> 迁移，而用户以为升级是自动的。

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

## 产物从哪来：`release.yml`

> v3.0 发布时产物是**没有**的：脚本解决的是「发布前该检查什么」，没解决
> 「产物从哪来」，用户拿到仓库只能自己从源码 build（issue #72）。

tag 推上去之后，`.github/workflows/release.yml` 被触发，构建并上传四类附件：

| 附件 | 内容 |
| --- | --- |
| `congorag_<版本>_<os>_<arch>.tar.gz` / `.zip` | api 与 worker 两个二进制（windows 是 zip），六个平台各一份 |
| `congorag-startup-<版本>-linux-<arch>.zip` | **启动包**：compose + `.env.example` + 镜像 tarball + 升级脚本 |

### 谁负责断言，谁负责构建

这条界线是刻意划开的，别把它们混起来：

- **`scripts/release.mjs` 负责断言。** CHANGELOG 有没有对应小节、契约
  `info.version` 与版本号是否一致、工作区是否干净、是否在 `main` 上、tag 是否
  已存在——**全部发生在推 tag 之前**，因为那是唯一能拦住一次错误发布的时刻。
- **`release.yml` 负责构建。** 它一个断言都不做：不打 tag、不改文件、不碰
  仓库状态，只把产物建出来挂上去。

为什么不让工作流也做断言：它跑在 tag 已经推上去之后，那时"版本号对不对"
已经没有意义了。两处都断言的结果是两边迟早不一致，而人只会看那个绿的。

为什么也不让 `release.mjs` 等这个工作流：那样发布脚本就要轮询 Actions API、
处理"排队""runner 挂了"这些和发布无关的状态。现在的耦合只有一个方向——
工作流的最后一步会**等 Release 对象出现**（`release.mjs` 先推 tag 再建
Release，两者之间有几十秒的窗口），超时就报错退出，不静默地少传文件。

### 构建失败时

不要删 tag 重推（那会让 Release 与 tag 的关系变得可疑）。用
**Actions → Release → Run workflow**，填上版本号重跑即可；上传步骤带
`--clobber`，重跑是幂等的。

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

## 发布后核对五件事

1. **Release 正文 == CHANGELOG 小节 + compare 链接**（脚本自动拼）。
2. **tag 指向的 commit 就是 `main` 顶端**：`git rev-parse 'v3.0^{commit}' main` 两行相同。
   **`^{commit}` 不能省**——两个 tag 都是 annotated，`git rev-parse v3.0` 给的
   是 tag 对象自己的 sha，不是它指向的 commit，不加会得到两个不同的哈希。
3. **CI 全绿。** 不写死 job 数量——它加过两次（`spec`、`docker`），而写死的数字
   没人会回来改。清单的唯一真相是 `.github/workflows/ci.yml`。
   注意 tag push **不触发** CI（`ci.yml` 的 `on` 只有
   `push[main]` / `pull_request` / `workflow_dispatch`），所以这一条要去看
   `main` 那次 push 的运行结果。tag push 触发的是 **Release** 那个工作流，
   两件事别混。
4. **契约 `info.version` == 版本号。**
5. **Release 上四个平台的二进制和两个架构的启动包都在**，见 Release 工作流
   那一次运行的结果。少了的话按上面的「构建失败时」重跑，不要手敲 `go build`
   补一个上去——手工补的产物和 CI 出来的不可比（工具链版本、`-trimpath`、
   `-ldflags` 都可能不一样）。

## 这个流程有意不做的事

- **不自动生成 CHANGELOG。** 从提交信息里凑出来的更新日志读起来像流水账，
  而读者要的是「升级之后我会遇到什么」。
- **不自动 bump 契约版本。** 脚本只断言，不改文件——发布前那一次修改是一个
  需要人看一眼的动作，而不是一次静默的字符串替换。
