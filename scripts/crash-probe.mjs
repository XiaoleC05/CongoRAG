#!/usr/bin/env node
/**
 * 崩溃探针（issue #62）。
 *
 * 制造一次**真实**的崩溃：在一轮 Agent 执行跑到一半时，给 api 进程一记
 * 不可捕获的 SIGKILL，然后读库，看崩溃现场留下了什么。用途是给 M4-C 的
 * Resume 准备一份"生效之前"的对照基线——没有它，resume 只能用单测构造
 * 状态来验证，而那些单测构造的正是你想要证明的东西。
 *
 * 【为什么不能用 Ctrl-C 冒充崩溃】
 *   · `docker compose up -d` 下 Ctrl-C 只影响发起它的那个终端，碰不到容器；
 *   · 就算 api 跑在前台，Ctrl-C 走的是 SIGINT，而 v3.0 已经实现了优雅退出
 *     （拒新请求 → 排空 SSE → worker 留收尾时间），那条路径会把它该写的
 *     终态写完再退——**恰好绕开 resume 要处理的所有情况**。
 * 所以这里只用 SIGKILL（Windows 上是 taskkill /F）——它不可捕获、不给进程
 * 任何 run 收尾的机会，进程地址空间直接消失。
 *
 * 【杀谁：两种形态都支持，判据完全相同】
 * 仓库里 api 现在**不在容器里**（deployments/docker/docker-compose.yml 只起
 * PostgreSQL，api 用 `make dev` 跑在宿主机上），而交付形态（启动包）里 api 在
 * 容器里。两种形态都得能制造崩溃，所以这里都实现了：
 *   · `docker:<容器名>` → `docker kill -s KILL <容器名>`
 *   · `pid:<PID>`       → win32 `taskkill /F /PID`；POSIX `kill -9`
 * **不指定就自动探测**：先看有没有名为 congorag-api 的容器在跑，有就走容器那条，
 * 否则找监听 api 端口的宿主机进程。
 *
 * 【两种形态为什么是等价的】SIGKILL 与 taskkill /F 都不可被捕获、都不会给
 * 目标进程任何清理机会——`docker kill -s KILL` 做的事就是把 SIGKILL 发给容器
 * 里的主进程，和直接对那个 pid 发 SIGKILL 是同一件事，只是多了一步转发。
 * 有差别的地方只有两处，都不影响判据：容器会留下一个 Exited 状态的容器对象
 *（宿主机进程不会），以及宿主机形态下没有任何东西会替你把它拉起来。
 *
 * 用法：
 *   node scripts/crash-probe.mjs                 # 自动探测，杀在第 1 步的一轮生成里
 *   node scripts/crash-probe.mjs --step 2        # 等第 1 步落库后再动手
 *   node scripts/crash-probe.mjs --kill docker:congorag-api
 *   node scripts/crash-probe.mjs --record backups/crash-baseline.md
 *   node scripts/crash-probe.mjs --help
 *
 * 【它需要什么】api 在跑（`make dev` 或启动包的 api 容器）、PostgreSQL 在跑、
 * 库里至少有一个 Agent、以及一个可用的 chat 模型——因为"跑到一半"必须真有一个
 * 在跑的 run。没有 chat 模型时 GET /api/v1/agents 还在，但 POST .../runs 会在
 * 落库之前就被拒（见 internal/agent/usecase.go 的 requireToolCapability：
 * 拒绝发生在 InsertRun 之前），这时脚本会明确报出"run 没起来"，而不是假装
 * 抓到了一次崩溃。
 */

import { execFileSync, spawn } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'

// ── 参数 ────────────────────────────────────────────────────────

const argv = process.argv.slice(2)

function argValue(name, fallback) {
  const i = argv.indexOf(name)
  return i !== -1 && argv[i + 1] ? argv[i + 1] : fallback
}
const hasFlag = (name) => argv.includes(name)

if (hasFlag('--help') || hasFlag('-h')) {
  console.log(
    [
      '崩溃探针：在 Agent 执行到一半时硬杀 api，然后读库看崩溃现场。',
      '',
      '  --step N          在第 N 步跑到一半时动手（默认 1）。判据是',
      '                    "current_step >= N-1 且 run 仍是 running"——',
      '                    即第 N 步已经开始、还没落库。',
      '  --delay MS        触发之后再等这么多毫秒才动手（默认 0，即一满足就杀）。',
      '                    加一点延迟是为了让它崩在**生成到一半**的时刻：',
      '                    output 里会有已经流出去的内容，那才是 resume 要复用、',
      '                    不该重生成的那部分。默认 0 时 output 通常是空的。',
      '  --kill TARGET     杀谁。docker:<容器名> 或 pid:<PID>。不填则自动探测。',
      '  --api URL         api 基址（默认 http://127.0.0.1:3210）。',
      '  --pg 容器名       PostgreSQL 容器名（默认 congorag-postgres）。',
      '  --input 文本      这次 run 的输入（默认一句必须调工具的话）。',
      '  --timeout MS      等 run 进入 running 的上限（默认 60000）。',
      '  --record 路径     把这次观测写成一份 markdown 基线记录。',
      '  --no-verify       只观测，不因为判据不成立而返回非零。',
      '',
      '退出码：0 = 抓到了崩溃且判据全部成立；1 = 没抓到或判据不成立；2 = 环境不对。',
    ].join('\n'),
  )
  process.exit(0)
}

const step = Number(argValue('--step', '1'))
const delayMs = Number(argValue('--delay', '0'))
const apiBase = argValue('--api', 'http://127.0.0.1:3210')
const pgContainer = argValue('--pg', 'congorag-postgres')
const input = argValue('--input', '3 个 128 的和乘以 2 是多少？先想一下再算。')
const timeoutMs = Number(argValue('--timeout', '60000'))
const killTarget = argValue('--kill', null)
const recordPath = argValue('--record', null)
const verify = !hasFlag('--no-verify')

if (!Number.isInteger(step) || step < 1) fail(2, `--step 必须是正整数，拿到的是 "${argValue('--step', '')}"`)

// 轮询间隔：api 的一轮生成大约 1 秒（本机实测 latencyMs=1091），
// 100ms 的粒度足够落在窗口里，又不会把 docker exec 打满。
const POLL_MS = 100
// 杀掉之后等一小会儿再读库：确认进程真的没了，也给"万一还有一次写"留出时间。
// 这一等的意义是让判据可信——立刻读的话，读到的可能是杀之前的那一刻。
const SETTLE_MS = 1500

// ── 小工具 ──────────────────────────────────────────────────────

function fail(code, message) {
  console.error(`\n✗ ${message}\n`)
  process.exit(code)
}

function info(message) {
  console.log(`· ${message}`)
}

/** 跑一个外部命令，返回 stdout（去尾换行）。失败抛异常。 */
function run(cmd, args) {
  return execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
}

/** 同 run，但失败返回 null 而不是抛（用于"探测"类调用）。 */
function tryRun(cmd, args) {
  try {
    return run(cmd, args)
  } catch {
    return null
  }
}

async function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms))
}

// ── 数据库（走容器，不走 PATH）───────────────────────────────────
//
// 【为什么不直接用 psql】本机没有 psql（`pg_dump`/`psql` 都不在 PATH 里），
// 能用的那一个是 PostgreSQL 容器里的。这和 scripts/upgrade.mjs 里
// "pg_dump 走容器"是同一个理由、同一个做法。
//
// `-tA -F '|'`：只要元组、不要表头、不要对齐空格——输出是给程序读的。

function sql(query) {
  return run('docker', [
    'exec', pgContainer,
    'psql', '-U', 'postgres', '-d', 'congorag',
    '-tA', '-F', '|', '-c', query,
  ])
}

/** 最新一条 run：返回 {id, status, currentStep} 或 null。 */
function latestRun() {
  const out = sql('SELECT id, status, current_step FROM agent_runs ORDER BY created_at DESC LIMIT 1')
  if (!out) return null
  const [id, status, currentStep] = out.split('\n')[0].split('|')
  return { id, status, currentStep: Number(currentStep) }
}

/** 某条 run 的全部 step，按 seq 升序。 */
function stepsOf(runId) {
  const out = sql(`SELECT seq, type, status FROM agent_run_steps WHERE run_id = '${runId}' ORDER BY seq`)
  if (!out) return []
  return out.split('\n').map((line) => {
    const [seq, type, status] = line.split('|')
    return { seq: Number(seq), type, status }
  })
}

// ── 杀谁 ────────────────────────────────────────────────────────

/**
 * 解析出这次要杀的进程标识。
 *
 * 【自动探测的顺序有意义】先看容器、再看端口。反过来不行：容器形态下
 * 宿主机上也有一份监听（Docker Desktop 的端口转发进程在听 3210），
 * 先看端口会杀到那个转发进程，容器里的 api 毫发无损——探针会以为
 * "杀掉了"，然后读到一份根本没崩的现场。
 */
function resolveTarget() {
  if (killTarget) {
    if (killTarget.startsWith('docker:')) return { mode: 'docker', name: killTarget.slice(7) }
    if (killTarget.startsWith('pid:')) return { mode: 'pid', pid: Number(killTarget.slice(4)) }
    fail(2, `--kill 只认 docker:<容器名> 或 pid:<PID>，拿到的是 "${killTarget}"`)
  }

  const container = tryRun('docker', ['ps', '--filter', 'name=congorag-api', '--format', '{{.Names}}'])
  if (container) {
    const first = container.split('\n')[0].trim()
    if (first) return { mode: 'docker', name: first }
  }

  const port = new URL(apiBase).port || '3210'
  const pid = findPidOnPort(port)
  if (pid) return { mode: 'pid', pid }
  fail(2, `没找到 api：既没有名为 congorag-api 的容器在跑，也没有进程在监听 ${port}。先 ` +
    '`make dev`（或起启动包的 api 服务），或者用 --kill 显式指定。')
}

function findPidOnPort(port) {
  if (process.platform === 'win32') {
    // netstat 的行长这样： TCP    127.0.0.1:3210    0.0.0.0:0    LISTENING    12345
    const out = tryRun('netstat', ['-ano', '-p', 'tcp'])
    if (!out) return null
    for (const line of out.split('\n')) {
      const cols = line.trim().split(/\s+/)
      if (cols.length < 5) continue
      if (cols[1].endsWith(`:${port}`) && cols[3] === 'LISTENING') return Number(cols[4])
    }
    return null
  }
  const out = tryRun('lsof', ['-nP', `-iTCP:${port}`, '-sTCP:LISTEN', '-t'])
  return out ? Number(out.split('\n')[0]) : null
}

/** 发一记 SIGKILL，然后确认目标真的死了。返回是否确认死亡。 */
async function killHard(target) {
  if (target.mode === 'docker') {
    // 就是这一条：不可捕获、不给任何收尾机会。
    run('docker', ['kill', '-s', 'KILL', target.name])
    for (let i = 0; i < 50; i++) {
      const running = tryRun('docker', ['inspect', '-f', '{{.State.Running}}', target.name])
      if (running === 'false') return true
      await sleep(100)
    }
    return false
  }

  if (process.platform === 'win32') {
    run('taskkill', ['/F', '/PID', String(target.pid)])
    // taskkill 自己就等不到进程退出才返回；这里再确认一次 PID 是否还在。
    for (let i = 0; i < 50; i++) {
      if (!tryRun('tasklist', ['/FI', `PID eq ${target.pid}`, '/NH'])?.includes(String(target.pid))) return true
      await sleep(100)
    }
    return false
  }

  run('kill', ['-9', String(target.pid)])
  for (let i = 0; i < 50; i++) {
    try {
      process.kill(target.pid, 0)
    } catch {
      return true
    }
    await sleep(100)
  }
  return false
}

function describeTarget(target) {
  return target.mode === 'docker'
    ? `docker kill -s KILL ${target.name}`
    : process.platform === 'win32'
      ? `taskkill /F /PID ${target.pid}`
      : `kill -9 ${target.pid}`
}

// ── 主流程 ──────────────────────────────────────────────────────

const target = resolveTarget()
const killedWith = describeTarget(target)

info(`api      ${apiBase}`)
info(`数据库    docker exec ${pgContainer} psql -U postgres -d congorag`)
info(`杀法      ${killedWith}`)
info(`时机      第 ${step} 步跑到一半（current_step >= ${step - 1} 且 run 仍是 running）`)

// 0. 前置检查：数据库连得上吗
// 【必须先查这一步】判据全靠读库，库不通的话后面每一条断言都会以
// "命令执行失败"的形式炸出来，而不是以"判据不成立"的形式——两类失败
// 要能分开看。
try {
  sql('SELECT 1')
} catch (err) {
  fail(2, `读不了数据库（docker exec ${pgContainer} psql ...）：${String(err.stderr ?? err.message).trim()}\n` +
    '  容器没起就 `make up`；容器名不是默认的就加 --pg <名字>。')
}

// 1. 前置检查：api 活着吗
try {
  const health = await fetch(`${apiBase}/healthz`, { signal: AbortSignal.timeout(5000) })
  if (!health.ok) fail(2, `api 的 /healthz 返回了 ${health.status}`)
} catch (err) {
  fail(2, `连不上 api（${apiBase}/healthz）：${err.message}`)
}
info('api 存活检查通过')

// 2. 选一个 agent
const agentsResp = await fetch(`${apiBase}/api/v1/agents`, { signal: AbortSignal.timeout(5000) })
if (!agentsResp.ok) fail(2, `GET /api/v1/agents 返回了 ${agentsResp.status}：${await agentsResp.text()}`)
const agents = await agentsResp.json()
if (!Array.isArray(agents) || agents.length === 0) {
  fail(2, '库里没有 Agent，先在界面上建一个（或 POST /api/v1/agents）。探针要有一条真的 run 才谈得上"跑到一半"。')
}
const agent = agents[0]
info(`用 Agent  ${agent.name}（${agent.id}）`)

// 3. 记下"动手之前"的库状态。判据要靠它区分"这条新 run"和历史上那些。
const before = latestRun()
info(`动手前的最后一条 run：${before ? `${before.id} status=${before.status} current_step=${before.currentStep}` : '（一条都没有）'}`)

// 4. 发起 run——**不 await 它跑完**，这正是重点：我们要在它跑到一半时杀掉进程。
//    SSE 连接被硬杀打断时 fetch 会抛，那是预期内的，单独 catch 掉。
let sseError = null
const runRequest = fetch(`${apiBase}/api/v1/agents/${agent.id}/runs`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
  body: JSON.stringify({ input }),
})
runRequest
  .then(async (resp) => {
    if (!resp.ok) {
      sseError = `HTTP ${resp.status}：${await resp.text()}`
      return
    }
    // 把流读干（读到被硬杀打断为止）。不读的话连接可能不推进。
    for await (const _ of resp.body) void _
  })
  .catch((err) => {
    sseError = err.message
  })

// 5. 轮询库，等第 N 步"已经开始、还没落库"的那一刻。
//
// 【为什么要轮询库而不是等 SSE 的某一帧】库才是判据的载体：崩溃之后
// SSE 连接已经没了，能回答"run 停在哪一步"的只有 agent_runs /
// agent_run_steps 两行。用同一个数据源做触发和判据，才不会出现
// "按 SSE 判它开始了、按库判它没开始"这种自相矛盾。
const deadline = Date.now() + timeoutMs
let caught = null
while (Date.now() < deadline) {
  const now = latestRun()
  if (now && now.id !== before?.id && now.status === 'running' && now.currentStep >= step - 1) {
    caught = now
    break
  }
  if (sseError) break
  await sleep(POLL_MS)
}

if (!caught) {
  const detail = sseError ? `run 请求失败了：${sseError}` : `等了 ${timeoutMs}ms 没等到符合条件的 run`
  console.error(`\n✗ 没抓到崩溃现场：${detail}`)
  console.error('  · 如果 run 请求 4xx：多半是没配 chat 模型，或那个 Agent 要的工具不可用。')
  console.error('  · 如果一直没进 running：把 --timeout 调大，或把 --step 调小到 1。')
  process.exit(1)
}

if (delayMs > 0) {
  info(`第 ${step} 步已经开始（current_step=${caught.currentStep}），再等 ${delayMs}ms 动手`)
  await sleep(delayMs)
}
info(`动手 —— ${killedWith}`)

// 6. 杀。
const diedCleanly = await killHard(target)
if (!diedCleanly) {
  console.error(`\n✗ 发了 SIGKILL 但没能确认目标已经退出（${killedWith}）。现场不可信，不继续判读。`)
  process.exit(1)
}
info('目标进程已确认退出')

// 7. 等一小会儿再读库。
await sleep(SETTLE_MS)

// 8. 读崩溃现场。
//
// 【为什么查 length(output) 而不是 output 本身】`-tA` 输出是按行的，而
// output 是多行文本——直接取它会把一条记录拆成好几行，后面按 '|' 切就全乱了。
// 长度在这里就够了：它回答的是"崩溃时已经流出去多少内容"，而这段内容
// 正是 resume 该复用、不该重生成的那部分。
const after = sql(`SELECT id, status, current_step, length(output) FROM agent_runs WHERE id = '${caught.id}'`)
  .split('\n')[0]
  .split('|')
const afterRun = { id: after[0], status: after[1], currentStep: Number(after[2]), outputLen: Number(after[3] ?? 0) }
const steps = stepsOf(caught.id)
const inFlightSeq = afterRun.currentStep + 1

// ── 判读 ────────────────────────────────────────────────────────
//
// 【判据只描述"崩溃现场必须长什么样"，不描述实现细节】issue #62 的验收原文
// 写的是「run 停在 running、该 step 没落库」，那是照着当时的实现写的——
// 当时 InsertStep 只在**一轮结束时**调用（internal/agent/usecase.go 的
// closeLLMStep），所以崩溃只可能让某一步整条缺失。
//
// 后来 Step 的落库时机变了（改成在一轮**开始**时就写一行 running），同样的
// 崩溃于是留下"一条停在 running 的 step"而不是"整条缺失"。两条都不违反
// "崩溃现场"的定义，所以下面 B 同时接受这两种形态、并把实际观察到的是
// 哪一种**打印出来**——那正是基线记录要留的东西。
//
// 这样写还有一层好处：哪天落库时机再变，探针不会开始报假警。真正会变红的
// 只有"崩在半途的那一步被记成了 completed"这种**语义**上的错误。

const inFlight = steps.find((s) => s.seq === inFlightSeq)
const inFlightShape = inFlight ? `已落库，停在 ${inFlight.status}` : '整条缺失'

const checks = [
  {
    name: 'A. run 停在 running（没有终态被补写）',
    ok: afterRun.status === 'running',
    got: `status=${afterRun.status}`,
  },
  {
    name: `B. 在飞的第 ${inFlightSeq} 步没有 completed 记录`,
    // 【这一条是崩溃的定义】进程在半途消失，那一步就不可能被标记成完成。
    // 它要么整条缺失（落库时机在轮末），要么停在 running（落库时机在轮首）。
    ok: !inFlight || inFlight.status !== 'completed',
    got: inFlightShape,
  },
  {
    name: 'C. 已落库的 step 里没有 pending 残骸',
    // pending 的含义是"创建了但从没开始"。崩溃发生在已经开始的那一步上，
    // 所以现场里不该出现 pending——出现它说明有一行是在别的时机写进去的。
    ok: steps.every((s) => s.status !== 'pending'),
    got: steps.length ? steps.map((s) => `${s.seq}:${s.type}:${s.status}`).join('  ') : '（一条 step 都没有）',
  },
]

console.log('\n════════ 崩溃现场 ════════')
console.log(`run id        ${afterRun.id}`)
console.log(`status        ${afterRun.status}`)
console.log(`current_step  ${afterRun.currentStep}`)
console.log(`output 长度   ${afterRun.outputLen} 字符（崩溃时已经流出去的内容）`)
console.log(`已落库 step   ${steps.length ? steps.map((s) => `${s.seq}/${s.type}/${s.status}`).join('  ') : '（一条都没有）'}`)
console.log(`在飞的那一步  seq=${inFlightSeq} —— ${inFlightShape}`)

console.log('\n════════ 判据 ════════')
let allOk = true
for (const c of checks) {
  console.log(`${c.ok ? '✓' : '✗'} ${c.name}   [${c.got}]`)
  if (!c.ok) allOk = false
}

if (recordPath) {
  const record = renderRecord({ killedWith, step, caught, afterRun, steps, checks, allOk })
  mkdirSync(dirname(recordPath), { recursive: true })
  writeFileSync(recordPath, record, 'utf8')
  console.log(`\n基线记录写到 ${recordPath}`)
}

if (!allOk && verify) {
  console.error('\n✗ 判据没有全部成立——这次抓到的现场不符合"崩溃"的定义，不要拿它当基线。')
  process.exit(1)
}

console.log('\n这次抓到的就是 resume 要处理的那类现场：run 行停在 running，在飞的那一步没有完成记录。')
// 【为什么强调"读在重启之前"】崩溃现场是一个**瞬时**状态。探针刻意在杀掉
// 进程之后、重启任何东西之前读库——一旦 api 重新起来，恢复入口（如果已经
// 实现）就会把这些行扫走（实测：上次留下的 running 行在下一次 api 启动时
// 变成了 interrupted）。晚一步读，看到的就不是现场而是恢复结果了。
console.log('注意：这个现场只在"进程死了、还没有东西重启它"的窗口里存在。')
console.log('     探针读库发生在杀掉之后、重启之前，所以看到的是现场本身。')
console.log('\n想把这次观测留成可对照的记录：加 --record <路径>。')
console.log('比对新旧记录的方法见 docs/testing.md 的「崩溃探针」一节。')
process.exit(0)

// ── 基线记录 ────────────────────────────────────────────────────
//
// 【为什么要有这个渲染】issue #62 要求"在还没写 resume 之前先跑一次并记录
// 输出，作为 resume 生效前后的对照基线"。记录必须自带当时的上下文（哪条
// 命令、哪个 commit、哪个时机），否则几个月后没人能判断两次记录的差异是
// 代码变了还是跑法变了。
function renderRecord({ killedWith, step, caught, afterRun, steps, checks, allOk }) {
  const commit = tryRun('git', ['rev-parse', '--short', 'HEAD']) ?? '(未知)'
  const lines = [
    '# 崩溃探针基线记录',
    '',
    `- 时间：${new Date().toISOString()}`,
    `- commit：${commit}`,
    `- 杀法：\`${killedWith}\``,
    `- 时机：第 ${step} 步跑到一半（动手时 current_step=${caught.currentStep}）`,
    `- 判据：${allOk ? '全部成立' : '**有未成立项**'}`,
    '',
    '## 崩溃后的库状态',
    '',
    '| 字段 | 值 |',
    '| --- | --- |',
    `| agent_runs.status | \`${afterRun.status}\` |`,
    `| agent_runs.current_step | ${afterRun.currentStep} |`,
    `| agent_runs.output 长度 | ${afterRun.outputLen} 字符 |`,
    `| agent_run_steps | ${steps.length ? steps.map((s) => `seq=${s.seq} ${s.type}/${s.status}`).join('；') : '（空）'} |`,
    `| 在飞的那一步（seq=${inFlightSeq}） | ${inFlightShape} |`,
    '',
    '## 判据',
    '',
    ...checks.map((c) => `- [${c.ok ? 'x' : ' '}] ${c.name} —— \`${c.got}\``),
    '',
    '## 怎么拿这份记录做对照',
    '',
    '这份记录是一份**快照**：它描述的是"那次运行时的代码 + 那次崩溃"留下的现场。',
    '判据本身只写"崩溃现场必须长什么样"，不绑实现细节；但 `agent_run_steps` 那一行',
    '会随落库时机变化（一轮**结束**时写 → 那一步整条缺失；一轮**开始**时写 → 停在 running），',
    '两种都不违反判据。所以比对新旧记录时，**先看判据那一节，再看具体形态**。',
    '',
    '1. run 行停在 `running` 是最关键的一条：没有任何东西替它补写终态。',
    '   优雅退出会补，所以它同时证明了"这次不是优雅退出"。',
    '2. 崩在半途的那一步不能是 `completed`——进程在半途消失，那一步就不可能被标记成完成。',
    '3. 已经流出去的 `output` 该不该被复用，取决于它有没有落库（见 docs/testing.md',
    '   里那条实测发现：`agent_runs.output` 只在轮次边界写入）。',
    '4. 两份记录都要留着。只留"改好之后"的那一份，就再也证明不了它改的正是当初那个现场。',
    '',
  ]
  return lines.join('\n')
}
