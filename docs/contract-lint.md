# 契约 lint（spectral）

`contracts/openapi.yaml` 是**唯一真相**：Go 的接口与 TS 的类型都从它生成。
这份文档记的是"它自己写得对不对"这件事由什么来拦。

## 它和另外两道门禁的分工

| 谁 | 回答什么问题 | 在哪 |
| --- | --- | --- |
| `contract` job | 改了契约**有没有重新生成** | ci.yml |
| `spec` job | 这份契约**本身**写得对不对 | ci.yml |
| 生成器（oapi-codegen / openapi-typescript） | 契约**能不能**生成出代码 | ci.yml 的 contract job |

关键的是第一行和第二行的区别：漂移检查对一份写错的契约完全无感——只要
生成物跟着同步了，`git diff --exit-code` 就是绿的。枚举写了两个一样的成员、
`$ref` 旁边挂了兄弟键、`example` 和 schema 对不上、新加的参数忘了写
description，这些都不会让生成器报错，只会让两端生成出一份错误的类型。

## 怎么跑

```bash
make lint-spec
```

它跑一条命令：

```bash
npx --yes --no-fund --no-audit @stoplight/spectral-cli@6.15.0 \
  lint contracts/openapi.yaml --ruleset .spectral.yaml --fail-severity=warn
```

**规则集、豁免、豁免理由全在 [`.spectral.yaml`](../.spectral.yaml) 里**，
不在 Makefile、也不在 CI 里。改门禁只改那一个文件。

版本锁死在 Makefile 的 `SPECTRAL` 变量和 ci.yml 的 `env.SPECTRAL_VERSION`
两处（写两遍的理由见 ci.yml 里 spec job 的注释：那个 job 刻意不装 Go，
所以不能走 `make`，而 Makefile 一开头就要 `go env GOPATH`）。

### `--fail-severity=warn` 不能省

oas 规则集里绝大多数检查的严重度是 warning，而 spectral **默认只在 error 上
返回非零**。不显式提这一档的话，这个 target 会对着所有真正有价值的问题保持
绿色——等于加了一个只看不说的门禁。

### 它不在 `make check` 里

`make check` 承诺的是"提交前快速自查、不联网、不依赖外部服务"；spectral 是
npx 拉下来的，首次跑必须联网。契约 lint 只需要在"改了
`contracts/openapi.yaml` 之后"和 CI 上跑。

## 当前状态：0 problems

规则集是官方的 `spectral:oas` 起步，关掉三条（见下）。**首次跑通的实测结果**：

```text
$ make lint-spec
No results with a severity of 'warn' or higher found!
```

也就是说，除了被豁免的三条，oas 规则集里其余 50 多条全部通过。这里面包括
几条真正有分量的：

- `oas3-schema` —— 整份文档的结构校验（JSON Schema 层面）。它红的时候，
  契约已经不是一个合法的 OpenAPI 文档了。
- `oas3-valid-schema-example` / `oas3-valid-media-example` —— 每个 `example`
  与它所在 schema 的一致性。**这一条特别值**：契约里的 example 是给人看的，
  而人会照着它写前端；一个和 schema 对不上的 example 会让人写出错误的代码，
  而且错的地方离原因很远。
- `operation-operationId-unique` / `path-params` / `no-$ref-siblings` /
  `array-items` —— 结构性的硬错误。
- `oas3-unused-component` —— 定义了但没人引用的组件。现在一条都没有。

## 豁免的三条，与各自的理由

三条有一个共同点：**它们的唯一消费者是文档站**（Swagger UI / Redoc 渲染出来
的那套页面）。本项目没有文档站，也不打算有——契约的读者是两端生成器和
`internal/` 下的实现，人的文档是 `docs/` 和契约里那些字段旁边的注释。

| 规则 | 它想要什么 | 为什么关掉 |
| --- | --- | --- |
| `operation-description` | 每个 operation 同时有 summary 和 description | 27 个 operation **全部**已经有 summary（`grep -c summary:` = `grep -c operationId:` = 27，一一对应）。没有文档站时 description 只会退化成把 summary 重写一遍——27 段重复文案，而且每次改接口都要跟着改 |
| `operation-tags` | 每个 operation 有非空的 tags 数组 | tags 的唯一作用是文档站里的分组导航。这份契约的"分组"已经由 URL 前缀（`/api/v1/agents`、`/api/v1/runs`）和注释分隔条承担了；加上 tags 会让同一个信息存在三处，而只有 tags 那一处没人读 |
| `info-contact` | info 块里有 contact（name/url/email 之一） | 这是本地优先的单机应用，契约描述的服务跑在用户自己机器上，不存在"对外提供 API 的服务方"，也就没有一个可以被联系的对象。归属与许可在仓库根的 LICENSE，找人的入口是 issue 页——两样都在契约之外，而且都是活的 |

完整的理由（包括每条"什么时候该回来重开"）写在 `.spectral.yaml` 里每条规则的
上方。

### 什么时候该回来重开

**要发布 API 文档站了**（或者要把契约交给外部消费者阅读）——那就是读者变了，
这三条从"重复数据"变成"缺的信息"。那时该做的是**补齐字段**，不是继续豁免。

## 这个门禁真的会咬人吗

会。三条负向验证（在**契约的副本**上做的，没有动 `contracts/openapi.yaml`）：

```text
# 1. version 去掉引号（YAML 会把它当浮点数）→ oas3-schema 报错，退出码 1
 12:12  error  oas3-schema  "version" property must be string.  info.version
✖ 1 problem (1 error, 0 warnings, 0 infos, 0 hints)

# 2. 枚举里塞一个重复项 → duplicated-entry-in-enum（warning）也会让退出码变 1
844:16  warning  duplicated-entry-in-enum  "enum" property must not have duplicate items ...  components.schemas.Message.properties.status.enum
✖ 1 problem (0 errors, 1 warning, 0 infos, 0 hints)
```

第 2 条是重点：**warning 级的问题同样让 job 变红**，这正是 `--fail-severity=warn`
存在的意义。

## 契约为什么没有顺手改

issue #68 的验收写的是"要么修契约里真正的问题，要么写豁免并说明理由"。
这里选了后者，理由是**范围**：豁免的三条要求的改动会波及 27 个 operation，
而 `contracts/openapi.yaml` 同时被 v4.0 里正在做的接口工作（#77 / #78 都要
往这份文件里加端点）编辑。为三条只服务文档站的 warning 去改 27 处，
换到的是一次几乎必然的合并冲突，和一份"为了过 lint 而写的"重复文案。

要补的话，那应该是一个**单独的动作**：等契约安静下来，按上面的"什么时候该
回来重开"补齐 tags 与 description，然后把 `.spectral.yaml` 里那三条删掉。
