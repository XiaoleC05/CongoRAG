# evals/

这个目录下有两个测量工具，它们回答的是两个不同的问题，**分母不是一回事**，
不要混着看：

| | `measure_recall.sh` | `run_eval.mjs` |
| --- | --- | --- |
| 测什么 | HNSW 近似索引相对精确扫描漏掉了多少分块 | 端到端：一份文档从上传到被引用，链路上每一段是不是通的 |
| 分母 | 精确扫描的 top-K | 人工写下的锚点（问题集里的标准答案片段） |
| 走哪条路 | 直接打 embedding API + `docker exec psql` | 只走产品面：HTTP + SSE |
| 要什么 | 数据库连接串、embedding 的 key | 一个跑起来的栈（api + worker）、配好的 provider |
| 单位 | 毫秒 + 百分比 | 若干指标（下面有说明） |
| 跑一次 | 秒级 | 分钟级，且要花钱 |
| 进 CI | 不进 | 不进（只有 `evals/lib/` 的单测进） |

一句话：`measure_recall.sh` 是"索引这种数据结构有没有损失"的性能测量，
`run_eval.mjs` 是"这个东西作为一个 RAG 产品能不能用"的端到端测量。
前者在后者全绿的时候也可能有问题（索引漏了但没漏掉被问到的那几条），
后者在前者全绿的时候也可能全是 0 分（链路断了、模型答非所问）。

## 怎么跑

三个终端（`make dev` 和 `make dev-worker` 都要在，少了 worker 文档会永远
停在 `queued`）：

```bash
make up           # ① PostgreSQL
make dev          # ② api，:3210
make dev-worker   # ③ 文档处理的消费端
```

四个环境变量：

```bash
export CONGORAG_API_BASE_URL=http://127.0.0.1:3210   # 必须是回环地址
export CONGORAG_EMBED_MODEL=BAAI/bge-m3              # 当前生效的 embedding 模型
export CONGORAG_EMBED_BASE_URL=https://api.siliconflow.cn/v1  # harness 不读，栈要用的
export CONGORAG_EMBED_API_KEY=sk-...                          # 同上
```

然后：

```bash
node evals/run_eval.mjs                # 跑一次，并自动跟 evals/baseline.json 对比
node evals/run_eval.mjs --write-baseline   # 把这次结果记成新基线
node evals/run_eval.mjs --repeat 3     # 连跑三遍，生成指标报均值和极差
node evals/run_eval.mjs --diff a.json b.json   # 只比两个已有报告，不跑栈
node evals/run_eval.mjs --help         # 全部选项和退出码
```

后两个环境变量 harness 自己不读，写在这里是因为**栈**要用它们（`make dev`
和 `make dev-worker` 从 `.env` 或 shell 里取），而 `CONGORAG_EMBED_MODEL`
是两边共用的：`measure_recall.sh` 用它筛 `embedding_model` 列，
`run_eval.mjs` 用它核对服务端当前生效的模型。

不设 `CONGORAG_EMBED_MODEL` 会直接退出（退出码 64），不会猜一个默认值——
embedding 模型换了之后，旧向量和新查询不在同一个空间里，检索结果基本
等于随机，那种状态下跑出来的数字不是"低"，是"没有意义"。

`--judge` 是可选的第二个模型裁判，默认关闭。它需要
`CONGORAG_CHAT_BASE_URL` / `CONGORAG_CHAT_API_KEY` / `CONGORAG_CHAT_MODEL`
三个变量。**这个开关写好之后没有真的跑过**，只做过语法和参数解析的检查。

### 本地单测（这个是进 CI 的那一半）

```bash
node --test "evals/lib/*.test.mjs"
```

**注意 `node --test evals/lib/` 在这台机器的 node v22.19.0 上不管用**——
实测它会把目录本身当成一个测试文件去跑，然后报
`ERR_TEST_FAILURE`/`test failed`，`# pass 0 / # fail 1`。原因是 node 22 把
`--test` 后面的位置参数当 glob 模式处理，`evals/lib` 这个模式匹配到的就是
那个目录。要么用上面的 glob（引号不能省，否则是 shell 展开而不是 node
自己展开），要么把两个文件显式列出来：

```bash
node --test evals/lib/score.test.mjs evals/lib/resolve.test.mjs
```

上面这两条（glob 和显式列文件）当前都是 `# tests 43 / # pass 43 / # fail 0`。

### 为什么全量 eval 不进 CI

它要真的起栈、真的调 embedding 和生成接口、真的花钱，而且**回答那一段
没有固定温度**——契约里 `SendMessageRequest` 只有 `text` 一个字段
（`contracts/openapi.yaml`），没有 `temperature`、没有 `seed`，同一个问题
两次跑会得到不同的措辞，facts 命中与否因此会随机翻转。

一个随机变红的门禁比没有门禁更糟：它会训练人忽略红色。所以 CI 里跑的
只有 `evals/lib/` 下的单测——纯算术 + 纯解析，输入是手写的 `.sse` 帧，
不联网、不花钱、结果确定。端到端那一半由人按需跑，跑完看结果文件。

## 数据集格式

`evals/dataset.jsonl`，一行一个 JSON 对象。选 JSONL 是因为加一个问题就是
一行 diff，不会像 JSON 数组那样把整个文件重排一遍。

```jsonc
{"id":"q001","class":"answerable","question":"...","anchors":["..."],"facts":["..."]}
{"id":"q015","class":"should_refuse","question":"...","anchors":[],"facts":[],
 "refusalMarkers":["没有","未提及","无法"]}
```

- `id` — 稳定且永不复用（`q001`、`q002`…）。复用会让"哪道题从通过翻成了
  不通过"这种对比指向错误的历史。
- `class` — `answerable` 或 `should_refuse`。
- `anchors` — **逐字**出现在语料里的片段。`answerable` 至少一条，
  `should_refuse` 必须为空（有锚点说明这题其实答得上）。
- `facts` — 必须出现在回答里的子串，大小写不敏感。
- `refusalMarkers` — 只给 `should_refuse` 用。

当前是 20 题：14 个 `answerable` + 6 个 `should_refuse`。语料是
`evals/corpus/` 下 5 份自己写的 markdown，git 跟踪、冻结不动。

### 为什么不用 chunk_id 当锚点

因为**分块 id 跨运行不稳定**，而且是设计使然：

- 主键是 `uuid.New()` 现生成的（`internal/retrieval/postgres.go`），
  `migrations/0001_init.up.sql` 里那一列是
  `uuid PRIMARY KEY DEFAULT gen_random_uuid()`
- 重建索引走的是"先删掉这份文档的全部分块，再插入新的"
  （`internal/retrieval/usecase.go`）

所以每一次重新上传、每一次重建索引（换 embedding 模型、修一次失败、
调整切分策略）、每一次任务重试，同一段内容都会换一个新 id。把 id 写进
数据集，等于把数据集绑死在一次特定的索引状态上——换一次模型，整份数据集
全红，而产品其实一点问题都没有。另外 `document_chunks` 没有顺序列、
`InsertChunks` 也不写 `created_at`（用的是建表时的 `DEFAULT now()`，一个
事务里所有行拿到的是同一个值），连"第几块"都不是稳定的。

跨运行稳定的只有**内容**。所以锚点是内容片段，不是 id。

锚点落在哪个语料文件上，是 harness 从语料里反查出来的，数据集里**不写
文件名**——写两遍必然有一份忘了改。反查结果可以直接看：

```bash
node evals/lib/resolve.mjs      # 打印语料指纹、题目计数、每题锚点来源
```

### 锚点长度为什么必须 ≤ 1200 字

`internal/knowledge/pipeline.go` 的切分策略是：**按空行分段，再贪心地把
段落装进不超过 `maxChunkChars`（= 1200）的块里**；只有单个段落本身超过
1200 字时才会退化成按字符数硬切。

由此可以推出一条保证：**一个不超过 1200 字的段落，必定完整地落在某一个
分块里，不会被切开**。于是"锚点是否出现在某个 citation 的 snippet 里"
就等价于"包含这段内容的那个分块有没有被检索到"。

这条保证是**依赖**，不是事实：它建立在上面的贪心切分策略之上。
`pipeline.go` 一旦改成分标题、按语义边界切、或者改小 `maxChunkChars`，
保证就没了，数据集必须重新核对。`evals/lib/resolve.mjs` 把 1200 硬编成
`MAX_ANCHOR_RUNES` 并在报错信息里直接指路 `internal/knowledge/pipeline.go`，
就是为了让这个依赖改起来能被看见。

## 指标

报告**强制分成两块**打印，因为它们的不确定性完全不同。

### 检索指标（确定性）

同样的语料 + 同样的 embedding 模型 + 同样的检索参数，这几项每次跑应当
逐位相同。它们只从 `citation` 事件算出来——服务端往 `snippet` 里填的是
分块的**完整正文**（`internal/ctxmgr/usecase.go`），所以整个比对在内存里
就能做完，不需要查库。

- `context recall@K`（K = 1/3/5）— 前 K 条引用里，有没有覆盖到这道题的
  锚点。分母是 `answerable` 的题数，不是引用条数。
- `MRR` — 第一条命中锚点的引用的排名倒数，对全部 `answerable` 题求平均。
  引用是按相关度从高到低到达的，所以排名有意义。

**名字里带 `context` 是刻意的，它不是一个检索器召回率。** 引用列表在预算
那一步被从头截断过（`internal/ctxmgr/usecase.go` 取的是有序前缀），所以
公式衡量的是"最终进入上下文的引用覆盖了多少锚点"——这是真实检索召回率的
**下界**，不是等于。想跟 `measure_recall.sh` 的数字对比的话，记住两者分母
完全不同。

**K 最大只到 5**，不是偷懒：`internal/retrieval/usecase.go` 的
`defaultTopK = 5`，而 `internal/conversation/usecase.go` 调 `Search` 时
没传 `TopK`（零值），`WithTopK` 在两个装配根里都没用过。产品面根本拿不到
第 6 条引用，recall@10 会是一列恒等于 recall@5 的数字。

### 生成指标（随机）

契约里没有温度，所以这几项每次跑都可能不一样。**单次的数字不要当结论**，
要看趋势就 `--repeat 3` 看极差。

- `refusal_accuracy` — `should_refuse` 的题里，回答里出现了拒答词的比例。
- `wrongly_refused_rate` — `answerable` 的题里，回答里出现拒答词的比例（越低越好）。
- `facts_hit_rate` — 声明了 `facts` 的 `answerable` 题里，`facts` 全部出现
  的比例。

拒答判据是一张**词表**（`没有`、`未提及`、`无法`、`不知道`…），不是模型
打分。这是刻意的：引入第二次模型调用会再加一层不确定性，而词表可复现、
可审计。代价是措辞不同的拒答会漏判，所以这个数只能当趋势看。

## 一次真实运行的结果

**还没有。** 写这份 README 的时候没有跑过全量 eval——它需要栈在跑、需要
真实的 embedding 和生成 API（要花钱），而这些都没有在这次的改动范围里。

所以：

- 上面那些指标，这一份 README 里**一个数字都不给**。
- `evals/baseline.json` 里 `status` 是 `not-yet-produced`，`metrics` 是
  `null`。那不是一份"0 分的基线"，是一个显式的占位——`run_eval.mjs` 认得
  这个状态，会明确拒绝对比，而不是把它当成 0 分（那样会让每一次运行都
  看起来像巨大进步）。

第一次真跑的人请用 `--write-baseline`，把结果贴回来替换这一节。

已经验证过的部分（这些是真跑过的，不是推测）：
`node --test "evals/lib/*.test.mjs"` 全绿（43/43）；`--help`、`--diff A B`、
参数错误（64）、栈不通（66）、embedding 模型不匹配（67）这几条退出码
都在一台真的起了栈的机器上核对过，其中模型比对的结果（`BAAI/bge-m3`，
维度 1024）来自真实的 `GET /api/v1/providers` 响应。**没有**验证过的是：
真的上传语料、真的等文档 ready、真的解析一条活着的 SSE 流。

## 语料小的时候不要相信 recall

和 `measure_recall.sh` 那边一样的老问题，换了个地方出现：**语料只有几 KB，
切出来的分块可能不超过 5 个，而 K 的上界也正好是 5。** 这时候每一次检索
都会把全部或近乎全部分块拿回来，`context recall@5` 天然等于 1，跟检索
质量没关系。

harness 会自己看着这件事：报告里有一行"观测到的不同引用片段数"，一旦它
不大于 K 就自动打一条警告。看到那条警告，说明这次跑的数字只能当"链路
是不是通的"来看——上传、切分、向量化、检索、引用下发这几段有没有接上，
而不能当"检索准不准"来看。

要让这张表变得有意义，需要的是**更长的语料**（几百到几千个分块），不是
更多的题目。题目加得再多，分块数不变的话 recall@5 还是恒等于 1。

## 结果文件

- `evals/results/<UTC 时间戳>-<commit 短哈希>.json` —— **本地产物，不进仓库**
  （`.gitignore` 里已忽略）。每次跑都会写一份，里面记了语料指纹、`git HEAD`、
  工作区当时是否干净、数据集每一行的哈希，以及逐题的明细。
- `evals/baseline.json` —— **这一份是要提交的**。它是被显式
  `--write-baseline` 写出来、被评审过的那一份，每次正常运行默认拿它做对比。

不在 git 仓库里（或没装 git）也能跑，只是报告里 `gitHead` / `gitClean`
两栏是 `null`。
