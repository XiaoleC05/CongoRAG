# 测试约定

这份文档回答两类问题，它们的共同点是「这个行为只在某个特定条件下才看得出
对错」，只是那个条件不同：

1. **条件 = 平台。** 详见下面的「规则」与「已有的案例」。
2. **条件 = 进程没有机会收尾。** 详见文末的「崩溃探针」——那边回答的是
   "被 SIGKILL 之后，数据库里剩下什么"。

起因是 `internal/knowledge/filestore.go` 的临时文件清理顺序（v2.0 的 #22）。那个缺陷
只在 Windows 上暴露，而 CI 跑在 `ubuntu-latest`——修复本身没有门禁，改回旧写法不会被
任何自动化发现，只有开发者的本机会红。这不是偶发问题：这个项目**开发在 Windows、
交付目标是 Linux 容器**，平台差异是结构性的，同类情况还会再出现。

## 规则

### 1. 优先消掉平台差异，而不是给每个平台各跑一遍

把平台相关的调用收敛到一个具名的、可替换的步骤里，然后断言那个步骤的**顺序或参数**。
这样一条测试在所有平台上都有效，而且失败信息指向具体的不变式，而不是"某个平台上红了"。

反例是把这类东西交给平台矩阵：矩阵让你知道"红了"，但不告诉你哪一条不变式破了，
而且它只在 CI 上红——开发者在本地复现不了（本机就装不了 `-race` 需要的 cgo 工具链）。

### 2. 接缝取最小粒度

能用函数参数表达，就不要给结构体加字段。

`closeAndRemoveTempFile(f tempFile, remove func(string) error)` 就是这条的样子：两个参数、
不改 `LocalFileStore` 的任何字段、不动 `port.go` 的 `FileStore` 接口。为了一条断言把整个
`os` 调用面抽成接口（`fileOps` 之类）是过度的；包级 `var osRemove = os.Remove` 更糟——
它污染全局状态，并发测试之间还会互相干扰。

### 3. 接缝旁边必须留一条跑真实实现的测试

接缝能被绕过：如果将来有人重写调用点、不再走那个具名步骤，接缝测试一条都不会红。

所以 `TestWriteTemp_WriteFails_LeavesNoTempFile` 保留着——它跑在真实文件系统上，
负责证明"接缝没有和真实调用脱节"。它的注释里写明了哪一半是平台相关的、为什么它自己
在 CI 上不构成门禁。**接缝测试与锚测试是一对，删掉任何一条都会留下一个盲区。**

### 4. 只在"被测差异是操作系统本身的事实"时才考虑平台矩阵

值得上矩阵的是这些：路径分隔符、大小写敏感性、换行符、**文件锁与删除语义**、
进程信号与退出码。

不值得的是"某段代码在某个平台上被跳过了"这类——那是规则 1 的活儿。

### 5. 平台矩阵的代价必须写出来

- Windows runner 的计费倍率是 Linux 的两倍（公开仓库免费，但私有仓库不是）。
- GitHub 的模块缓存键含 OS，新增一个平台首次会全量拉依赖。
- `-race` 需要 cgo + gcc。**本机实测没有 gcc 且 `CGO_ENABLED=0`**，所以带 `-race` 的
  那条腿在 Windows runner 上是否跑得起来并没有被验证过；最坏的结果是要么那条腿一直红、
  要么被迫在 Windows 上丢掉 `-race`，反而削弱该 job。

加平台矩阵之前，先说明这些代价换到了什么。

### 6. 只在某个平台会红的测试，必须点名说出来

测试注释里要写清"这条只有在 X 上才会在修复前变红"，并且在 CHANGELOG 的
「行为变更」或发布说明的「已知限制」里记一笔。信息只在某台机器上的话，等于没有。

### 7. 每条平台相关的分支，都要在 PR 里说明用哪一条覆盖

包括"故意不覆盖"的决定。**不写下来，下一个人会以为它被覆盖了。**

## 已有的案例

| 位置 | 平台相关的是什么 | 用了哪几条规则 |
| --- | --- | --- |
| `internal/knowledge/filestore.go`（v2.0 #22 / v3.0 #51） | Windows 上句柄未关时 `os.Remove` 因共享冲突失败；POSIX 允许 unlink 已打开的文件 | 1、2、3、6 |
| `Makefile` 的 `test-integration`（v3.0 #46） | 本机 `-race` 不可用（无 gcc / `CGO_ENABLED=0`），CI 上必须开 | 5 |
| `internal/platform/secretbox.go`（文件权限 0600） | Windows 上 `os.Chmod` 基本是空操作，POSIX 才真正生效 | 6 |

## 不做的事

- **不给每个平台各写一份测试。** 规则 1 能覆盖的，就不该变成两份。
- **不让测试去探测当前平台来决定跳过。** 那种测试在被跳过的平台上永远绿，
  和没有测试是一样的效果。

---

## 崩溃探针（issue #62）

上面讲的是「平台差异怎么被覆盖」。这一节讲的是开头说的第二类条件——
**进程被杀掉之后，数据库里剩下什么**。它属于同一份文档，因为它要回答的
也是"这个行为只在某个特定条件下才看得出对错"。

### 怎么用

```bash
make dev                                  # 另开一个终端：api 必须在跑
make crash-probe                          # 自动探测杀谁，默认杀在第 1 步的一轮生成里
make crash-probe PROBE_ARGS="--step 1 --delay 1200 --record backups/crash-baseline.md"
node scripts/crash-probe.mjs --help       # 全部参数
```

前置条件三个：api 在跑、PostgreSQL 在跑、库里有一个 Agent 且配了可用的 chat
模型。缺任何一个脚本都会明确说出来，不会假装抓到了一次崩溃。

退出码：`0` = 抓到了崩溃且三条判据全部成立；`1` = 没抓到或判据不成立；
`2` = 环境不对（连不上库 / 找不到 api）。

### 为什么不能用 Ctrl-C

这是整个探针存在的理由，也是它最容易被人"顺手简化"掉的地方：

- `docker compose up -d` 下 Ctrl-C **碰不到容器**——它只影响发起它的那个终端。
- 就算 api 跑在前台，Ctrl-C 走的是 SIGINT，而 v3.0 已经实现了优雅退出
  （拒新请求 → 排空 SSE → worker 留收尾时间）。那条路径会把它该写的终态
  写完再退，**恰好绕开 resume 要处理的所有情况**。

所以只认 SIGKILL（Windows 上是 `taskkill /F`）：不可捕获、不给进程任何
收尾机会。

### 杀谁：两种形态，判据相同

仓库里 api 现在**不在容器里**（`deployments/docker/docker-compose.yml` 只起
PostgreSQL，api 用 `make dev` 跑在宿主机上），而交付形态（启动包）里 api 在
容器里。脚本两条路都实现了：

| 形态 | 命令 |
| --- | --- |
| 容器 | `docker kill -s KILL <容器名>` |
| 宿主机进程 | Windows `taskkill /F /PID <pid>`；POSIX `kill -9 <pid>` |

**不指定就自动探测，先看容器再看端口。** 顺序不能反——容器形态下宿主机上
也有一份监听（Docker Desktop 的端口转发进程在听同一个端口），先看端口会杀到
那个转发进程，容器里的 api 毫发无损，而探针会以为"杀掉了"，然后读到一份根本
没崩的现场。

两种形态等价：`docker kill -s KILL` 做的事就是把 SIGKILL 转发给容器里的主进程，
和直接对那个 pid 发 SIGKILL 是同一件事，只是多一步转发。差别只有两处，都不
影响判据：容器会留下一个 Exited 状态的容器对象（宿主机进程不会），以及宿主机
形态下没有任何东西会替你把它拉起来。

### 三条判据

**判据只描述"崩溃现场必须长什么样"，不描述实现细节。** 这一点是有教训的：
issue #62 的验收原文写的是「该 step 没落库」，那是照着当时的实现写的——当时
`InsertStep` 只在一轮**结束**时调用（`internal/agent/usecase.go` 的
`closeLLMStep`），所以崩溃只可能让某一步整条缺失。后来落库时机改成一轮
**开始**时就写一行 `running`，同样的崩溃于是留下"一条停在 running 的 step"。
两种都不违反"崩溃现场"的定义，所以判据按语义写、把形态打印出来。

| # | 判据 | 为什么是这条 |
| --- | --- | --- |
| A | `agent_runs.status` 仍是 `running` | 没有任何东西替它补写终态。优雅退出会补，所以这条恰好证明了"这次不是优雅退出" |
| B | 在飞的那一步（`seq = current_step + 1`）**没有 `completed` 记录** | 进程在半途消失，那一步就不可能被标记成完成。它要么整条缺失（落库在轮末），要么停在 `running`（落库在轮首）——两种都算数，脚本会把实际观察到的是哪一种打出来 |
| C | 已落库的 step 里没有 `pending` 残骸 | `pending` 的含义是"创建了但从没开始"。崩溃发生在已经开始的那一步上，所以现场里不该出现它 |

这样写的好处是：落库时机再变，探针不会开始报假警；真正会变红的只有
"崩在半途的那一步被记成了 `completed`"这种**语义**上的错误。

### 两次实测记录（2026-09-22）

issue #62 要求"在还没写 resume 之前先跑一次并记录输出，作为 resume 生效前后的
对照基线"。下面是两次真实的观测，中间 Step 的落库时机变了——**保留两份**是
刻意的：只留"改好之后"的那一份，就再也证明不了它改的正是当初那个现场。

**第一次（`--kill pid:44532`，宿主机形态、真实模型、真实库）：**

```text
════════ 崩溃现场 ════════
run id        07458de4-2967-4fc1-81a4-76cd6bcc102f
status        running
current_step  0
output 长度   0 字符
已落库 step   （一条都没有）

════════ 判据 ════════
✓ A. run 停在 running（没有终态被补写）   [status=running]
✓ B. 在飞的第 1 步没有落库   [已落库的 seq=[]]
✓ C. 已落库的 step 里没有 running/pending 残骸   [（一条 step 都没有）]
```

**第二次（同一条命令，之后代码变了）：**

```text
run id        27e4fb6c-1f0a-4b2c-9acd-f688ffd0d6e8
status        running
current_step  0
output 长度   0 字符
已落库 step   1/llm/running
在飞的那一步  seq=1 —— 已落库，停在 running

════════ 判据 ════════
✓ A. run 停在 running（没有终态被补写）   [status=running]
✓ B. 在飞的第 1 步没有 completed 记录   [已落库，停在 running]
✓ C. 已落库的 step 里没有 pending 残骸   [1:llm:running]
```

差异只有 `agent_run_steps` 那一行：第一次是"整条缺失"，第二次是"停在
running"。**A 两次都一样，而 A 才是崩溃的定义。**

两次都自动探测到了要杀谁（第一次是显式指定的 pid，第二次走的是默认的
"先看容器再看端口"那条路）。

**这次实测顺带查到一件事，对 resume 的设计是硬约束：`output 长度 = 0`。**

`agent_runs.output` 只有一个写入点——`PgCheckpointStore.Save`
（`internal/agent/postgres.go`），而调用它的 `checkpoint()` 只在**轮次边界**
上跑。也就是说**一轮生成进行到一半时，已经流出去的 token 只存在于 SSE 连接里，
库里一个字都没有**。加 `--delay 1200` 再跑一次，`output 长度` 依然是 0——延迟
不是变量，写入时机才是；后面 Step 落库时机变了，这一条也没跟着变（第二次
实测的 `output 长度` 仍然是 0）。

这一条直接决定了 resume 能复用多少：光读 `agent_runs.output` 拿不到半截回答，
要么把 checkpoint 提到每一轮**开始**（代价是每个 token 一次 UPDATE），要么
接受"崩在生成中途的那一轮必须整轮重生成"并把它写进恢复语义。issue #66 里
"interrupted 的三行崩溃表"说的正是这件事，基线数据在这里。

### 现场是瞬时的

**崩溃现场只在"进程死了、还没有东西重启它"的窗口里存在。** 探针读库发生在
杀掉之后、重启任何东西之前，所以它看到的是现场本身；而一旦 api 重新起来，
恢复入口就会把这些行扫走。实测过一次：第一次留下的那条 `running` 行，在
下一次 api 启动之后变成了 `interrupted`。

这带来两个使用上的注意：

- **要留证据就在探针跑完之前留**（`--record <路径>` 把观测写成 markdown）。
  重启之后再去看库，看到的已经是恢复结果，不是现场。
- **别拿重启之后的库状态去判断探针有没有工作。** 判断依据是探针自己打印的
  那三条判据。

除此之外探针没有别的残留：它只读文件系统、只通过 SSE 读 api，不往 `data/`
里写任何东西。留在 `agent_runs` 里的那条行是**证据**，不是垃圾——它会被
恢复入口接管，不需要手工清库。

## 恢复的端到端验证（issue #64）

崩溃探针回答的是"崩了之后库里剩什么"。这一节回答的是它的下一句：
**那些残留，恢复入口能不能真的接上。**

单元测试做不到这件事——它们构造的正是"你想证明的那个现场"，所以
issue #64 的验收标准写的是"配合崩溃探针做一次端到端验证（4 步 run 在第 3 步
被杀 → 重启从第 4 步继续）"。下面是 2026-09-22 真实跑通的一次，命令与输出
都是原样抄下来的。

### 怎么跑

```bash
# 1. 起 api（宿主机形态）
make dev
# 2. 造一个只用 calculator 的 Agent，然后：
node scripts/crash-probe.mjs --api http://127.0.0.1:3210 --kill pid:<api进程>
# 3. 重启 api —— 启动扫描会把留下的 run 标成 interrupted
# 4. 调恢复
curl -N -X POST http://127.0.0.1:3210/api/v1/runs/<runId>/resume
```

### 实测记录

崩溃现场（`--step 2`，真实模型、真实库）：

```text
run id        89e96dfd-c4bd-4115-a7de-8ac03ff3b1fd
status        running
current_step  4
已落库 step   1/llm/completed  2/tool/completed  3/tool/completed  4/tool/completed  5/llm/running
在飞的那一步  seq=5 —— 已落库，停在 running
```

重启之后、恢复之前：

```text
恢复前: status=interrupted currentStep=4        ← 启动扫描干的（ADR-007 决策二）
```

调 `resume`（响应是 SSE，第一帧永远是 `run_started`）：

```text
=== 帧类型 ===
      1 event: done
      1 event: run_started
     49 event: token
```

恢复之后：

```text
status=completed currentStep=6 outlen=59

=== steps ===
1 llm completed
2 tool completed calculator
3 tool completed calculator
4 tool completed calculator
5 llm interrupted          ← 崩在半途的那一轮，标 interrupted，不假装成功
6 llm completed            ← 恢复跑出来的那一轮，接在已有编号之后

=== tool_effect_log === 3 行（崩溃前后一样）
=== 错误日志 === （空——没有 duplicate key，也没有静默失败）
```

### 这次验证查出的一个真缺陷（已修）

第一次跑的时候，恢复「成功」了（run 进入 `completed`、流也正常收尾），
但**恢复跑出来的那一步在轨迹里彻底消失**，而且 `current_step` 被倒着改回了 `1`。
根因写在 api 日志里那一行：

```text
level=ERROR msg="failed to record agent run step" ... seq=1 type=llm
  error="insert step 1 of run ...: duplicate key: agent_run_steps_run_seq_unique"
```

`consumeEvents` 的 Step 编号从 1 开始，而恢复是接着一条**已经有 seq 1..N 的
run** 往下跑——第一条 INSERT 撞上 `UNIQUE (run_id, seq)`，而轨迹写入不是致命
路径，那个失败只被记成一行日志。

**它不会让任何单元测试变红**（那些测试都是"从头跑一次"，编号本来就从 1 开始）。
这正是 issue #64 要求"配合崩溃探针做端到端验证"的原因：单元测试构造不出
"接着已有的编号往下跑"这个形状。

修法是让编号**从数据里推导**（`Repo.MaxStepSeq`），而不是让调用方算好一路
当参数传下去——传参的版本有三个地方可以漏，而漏掉的表现恰好是静默的。
`internal/agent/recovery_test.go` 的
`TestResume_StepNumberingContinuesAfterExistingSteps` 钉住它，
并且**做过变异检验**（把 `MaxStepSeq` 改成永远返回 0，那条测试会红）。
