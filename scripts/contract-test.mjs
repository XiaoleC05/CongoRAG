#!/usr/bin/env node
/**
 * 契约测试（issue #69）——让真实的 api 回答一遍，用 Prism 逐条校验
 * 请求与响应是否符合 `contracts/openapi.yaml`。
 *
 * ── 它补的是哪一段空白 ────────────────────────────────────────
 *
 * CI 里那条 `make generate + git diff --exit-code` 回答的是
 * 「**改了契约有没有重新生成**」。它对一份"生成物同步、但 handler 实际返回
 * 的结构和契约不一致"的代码完全无感：漏字段、状态码不对、错误形状走偏，
 * 生成器一个都拦不住。这一条补的就是运行时那一段。
 *
 * ── 为什么必须是 proxy 模式 ───────────────────────────────────
 *
 * Prism 的 mock 模式只读 yaml、自己按 schema 编一个响应，然后拿那个响应去
 * 校验自己——**永远绿**。proxy 模式把请求转发给真实的 api，再拿**真实响应**
 * 去对照契约，才能测出东西。开发文档 §10.3 把这条列成陷阱表的第一行。
 *
 * ── 它起什么 ──────────────────────────────────────────────────
 *
 *   1. 编译并启动 api（`go build` 一次，不用 go run——它每次都要重新编译，
 *      而这里要等它听端口）；
 *   2. 起 prism proxy，指向那个 api；
 *   3. 通过 **prism** 发一串请求（而不是直接打 api）；
 *   4. 停掉 prism，检查它有没有报过校验违规。
 *
 * 【请求集为什么是这些】覆盖到"不需要配置模型就能答"的全部端点——配置
 * provider / 发消息 / 跑 Agent 都会打真实的模型服务，而这个脚本要在 CI 上
 * 无人值守地跑。没覆盖的端点在下面的 NOT_COVERED 里列着，不是忘了。
 *
 * 用法：
 *   node scripts/contract-test.mjs
 *   node scripts/contract-test.mjs --db "postgres://..." --keep-going
 *   node scripts/contract-test.mjs --help
 *
 * 【它需要什么】PostgreSQL 在跑（迁移已应用）、node、Go 工具链，以及一次
 * 能拉下 prism 的网络（npx 缓存过之后就不用了）。
 */

import { spawn, spawnSync } from 'node:child_process'
import net from 'node:net'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fail, info, ok, step } from './delivery-lib.mjs'

// Prism 的版本锁死，理由与 ci.yml 里那两个 Go 工具相同：prism 换了小版本，
// 校验的严格程度就可能变，而这个 target 的红绿要只反映"契约与实现对不上"。
const PRISM = '@stoplight/prism-cli@5.14.2'

const argv = process.argv.slice(2)
const hasFlag = (name) => argv.includes(name)
function argValue(name, fallback) {
  const i = argv.indexOf(name)
  return i !== -1 && argv[i + 1] ? argv[i + 1] : fallback
}

if (hasFlag('--help')) {
  console.log(`用法：node scripts/contract-test.mjs [选项]

  --db <url>      api 连哪个库（默认取 CONGORAG_DB_URL，再退回开发库）
  --api-port <n>  api 监听端口（默认 3211，故意避开 make dev 的 3210）
  --prism-port <n> prism 监听端口（默认 4011）
  --keep-going    某个请求失败时继续跑完剩下的，而不是立刻停
  --help          打印这段

退出码：0 = 全部通过；1 = 有校验违规或请求失败；2 = 前置条件不满足。`)
  process.exit(0)
}

const dbURL =
  argValue('--db') ||
  process.env.CONGORAG_DB_URL ||
  'postgres://postgres:postgres@127.0.0.1:5432/congorag?sslmode=disable'
const apiPort = Number(argValue('--api-port', '3211'))
const prismPort = Number(argValue('--prism-port', '4011'))
const keepGoing = hasFlag('--keep-going')
const prismBase = `http://127.0.0.1:${prismPort}`

// 【为什么故意换端口】`make dev` 通常占着 3210。这个脚本会起自己的 api，
// 撞上端口的话报出来的是 EADDRINUSE，而真实原因（"你另一个终端还开着"）
// 得绕一圈才想得到。
const apiBase = `http://127.0.0.1:${apiPort}`

const repoRoot = new URL('..', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1')

/**
 * 这一轮往库里造了哪些东西。
 *
 * 【为什么失败时也要清】第一版只在请求集跑完之后删，于是**中途失败的每一次
 * 运行都会留下一条知识库**（调试这个脚本的那几次一共留了 4 条在开发库里，
 * 手工才清掉）。清理不该依赖"跑成功了"。
 */
const created = { kbIDs: [], conversationIDs: [] }

/**
 * 断言失败但 --keep-going 时攒在这里。
 *
 * 【为什么要有这个开关】契约一改，可能一次漂移好几个端点；立刻停会让你
 * 修一个、再跑一次、再看到下一个。攒起来一次看完更省事。
 * 需要前置结果的地方（比如没有 kbId 就没法测后面的）仍然会立刻停——
 * 那种情况下继续跑没有意义。
 */
const batteryFailures = []

/** 尽力删掉这一轮造的数据。删不掉只记一行，不改变退出码。 */
async function cleanupCreated(prismHandle) {
  if (!prismHandle?.child || prismHandle.child.exitCode !== null) return
  for (const id of created.conversationIDs) {
    try {
      await fetch(`${prismBase}/api/v1/conversations/${id}`, { method: 'DELETE' })
    } catch {
      // 尽力而为：脚本可能正处在"prism 已经挂了"的状态里。
    }
  }
  for (const id of created.kbIDs) {
    try {
      await fetch(`${prismBase}/api/v1/knowledge-bases/${id}`, { method: 'DELETE' })
    } catch {
      // 同上。
    }
  }
}

/**
 * 起一个子进程并把它的输出收进内存（后面要扫 prism 的日志）。
 *
 * 【shell 只在必须时开】Windows 上 `npx` 是个 `.cmd`（要 shell 才找得到），
 * 而 api 是我们自己刚编出来的绝对路径 `.exe`（不需要 shell）。多一层 cmd
 * 包装层的代价是**多一层进程**：包装层可能在真正的进程之前就退出，
 * 于是"杀进程"变成"杀了个已经死了的壳"，真身留下来继续占着端口和管道
 * （实测踩过，见 stopProcess 的注释）。
 */
function startProcess(cmd, args, opts = {}) {
  const { useShell = false, ...rest } = opts
  const child = spawn(cmd, args, {
    cwd: repoRoot,
    stdio: ['ignore', 'pipe', 'pipe'],
    shell: useShell,
    ...rest,
  })
  const lines = []
  const collect = (stream) => {
    stream.setEncoding('utf8')
    stream.on('data', (chunk) => {
      for (const line of chunk.split(/\r?\n/)) {
        if (line.trim()) lines.push(line)
      }
    })
  }
  collect(child.stdout)
  collect(child.stderr)
  return { child, lines }
}

/** 轮询一个 URL 直到它答 200，或者超时。 */
async function waitForHttp(url, timeoutMs, what) {
  const deadline = Date.now() + timeoutMs
  let lastErr = null
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return res
      lastErr = new Error(`HTTP ${res.status}`)
    } catch (err) {
      lastErr = err
    }
    await new Promise((r) => setTimeout(r, 250))
  }
  throw new Error(`${what} 没能在 ${timeoutMs / 1000}s 内就绪（最后一次：${lastErr?.message ?? '未知'}）`)
}

/**
 * 停掉一个子进程。
 *
 * 【三条都不能省，每一条都对应一次实测故障】
 *
 *  1. **按进程树杀**（Windows 的 `/T`、POSIX 直接 SIGKILL）。只杀直接子进程
 *     会漏掉 `npx` 拉起来的那个真正的 prism——它继承着我们的管道。
 *  2. **不要因为"子进程已经退出"就提前返回**。上一次的写法在这里 return 了，
 *     而 `npx` 的包装层会先退出、真身还在，于是杀不掉它。判据必须是"端口上
 *     还有没有东西"，而不是"句柄死没死"。
 *  3. **主动断开管道**。即使有漏网的进程，它也不该把 node 的事件循环拖住——
 *     症状是脚本把结果全打印完了、就是不退出（在 make 里表现为挂满超时）。
 */
function stopProcess(handle) {
  const child = handle?.child
  if (!child) return
  try {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/F', '/T', '/PID', String(child.pid)], { stdio: 'ignore' })
    } else {
      child.kill('SIGKILL')
    }
  } catch {
    // 杀不动也没关系，下面的断开管道保证脚本能退出。
  }
  child.stdout?.destroy()
  child.stderr?.destroy()
  child.unref()
}

/**
 * 通过 prism 发一个请求，返回 { status, body, raw }。
 *
 * 【为什么不直接用 fetch 打 api】那样 prism 什么都看不到——它只校验经过
 * 自己的流量。所有请求都必须走 prism 那一跳，这正是"proxy 模式"的含义。
 */
async function call(method, path, { body, headers = {}, expect } = {}) {
  const res = await fetch(prismBase + path, {
    method,
    headers: { ...(body ? { 'Content-Type': 'application/json' } : {}), ...headers },
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  let parsed = null
  try {
    parsed = text ? JSON.parse(text) : null
  } catch {
    // SSE 流、空 body、HTML 兜底页都会走到这里——原样留着给断言看。
  }
  const result = { status: res.status, body: parsed, raw: text }
  if (expect !== undefined && res.status !== expect) {
    const msg = `${method} ${path} 期望 ${expect}，实际 ${res.status}：${text.slice(0, 200)}`
    if (!keepGoing) throw new Error(msg)
    batteryFailures.push(msg)
  }
  return result
}

/**
 * 请求集。
 *
 * 【每个条目的 exists 为 false 时表示"这个端点本该答 404"】——错误响应
 * 的形状（application/problem+json 的 Problem）也在契约里，一样要校验。
 * 只测成功路径的话，错误形状走偏了没有任何东西会发现。
 */
async function runBattery() {
  step('通过 prism 打真实 api')

  const results = []
  const record = (name, flag) => results.push({ name, flag })

  // ── 健康检查 ──
  await call('GET', '/healthz', { expect: 200 })
  record('GET /healthz', 'ok')
  await call('GET', '/readyz', { expect: 200 })
  record('GET /readyz', 'ok')

  // ── 知识库 CRUD 走一整圈 ──
  const kbResp = await call('POST', '/api/v1/knowledge-bases', {
    body: { name: `契约测试-${Date.now()}` },
    expect: 201,
  })
  record('POST /api/v1/knowledge-bases', 'ok')
  const kbId = kbResp.body?.id
  if (!kbId) throw new Error('新建知识库的响应里没有 id——契约里它是 required')
  created.kbIDs.push(kbId)

  await call('GET', `/api/v1/knowledge-bases/${kbId}`, { expect: 200 })
  record('GET /api/v1/knowledge-bases/{id}', 'ok')

  await call('GET', '/api/v1/knowledge-bases?limit=5', { expect: 200 })
  record('GET /api/v1/knowledge-bases（分页信封）', 'ok')

  // 【这里是 204 不是 200，也不要"顺手"改成 200】契约里写明了为什么：
  // 客户端本来就知道新名字（是它自己传上来的），服务端不必再查一次库、
  // 把对象回给它。这一条是运行起来才发现的——我第一版按 200 写，第一次跑
  // 就红了，而**红的是脚本、不是实现**（契约那边是 204）。契约测试的价值
  // 有一半就在这种地方：它逼你去读契约，而不是按印象写。
  await call('PATCH', `/api/v1/knowledge-bases/${kbId}`, {
    body: { name: `契约测试-改名-${Date.now()}` },
    expect: 204,
  })
  record('PATCH /api/v1/knowledge-bases/{id} → 204', 'ok')

  await call('GET', `/api/v1/knowledge-bases/${kbId}/documents?limit=5`, { expect: 200 })
  record('GET /api/v1/knowledge-bases/{id}/documents', 'ok')

  // 检索调试：库里没有任何向量，所以命中的是**空数组**那条分支
  // （契约里 hits 是数组、可以是空的）。这条同时钉住了"空数组不是 null"。
  const search = await call('POST', `/api/v1/knowledge-bases/${kbId}/search`, {
    body: { query: '契约测试', topK: 3 },
    expect: 200,
  })
  if (!Array.isArray(search.body?.hits)) {
    throw new Error('POST .../search 的 hits 不是数组——契约里它是 required array')
  }
  record('POST /api/v1/knowledge-bases/{id}/search', 'ok')

  // ── 错误形状 ──
  //
  // 【为什么 400 那条用的是"游标解不出来"，而不是"名字为空"】
  // 后者违反的是请求 schema 自己的 `minLength: 1`——而 prism 带 `--errors`
  // 时会**先把它拦下来**（返回它自己的 422），请求根本到不了 api。这一条
  // 顺带说明了分工：**能由 schema 表达的非法输入，由契约在入口挡掉；
  // 只有表达不了的（比如"这个游标不是我们发的"）才落到 api 的 400。**
  //
  // 所以下面这条特意挑了一个 **schema 合法**的输入：cursor 是自由字符串
  // （只限长度），`not-a-cursor` 完全合法，解不出来是 api 自己的判断。
  //
  // 【为什么是 /conversations 而不是 /knowledge-bases】知识库列表**没有**
  // 分页参数——契约里它没有 limit/cursor，多传一个 cursor 会被 gin 忽略、
  // 照样 200。这个是第一次跑才发现的（我原本写的就是知识库列表，
  // 期望 400 却拿到 200）。
  await call('GET', `/api/v1/knowledge-bases/${crypto.randomUUID()}`, { expect: 404 })
  record('GET 不存在的知识库 → 404 Problem', 'ok')

  await call('GET', '/api/v1/conversations?cursor=not-a-cursor', { expect: 400 })
  record('GET 坏游标 → 400 invalid_argument（schema 合法、api 自己判的）', 'ok')

  // ── 会话 ──
  const conv = await call('POST', '/api/v1/conversations', { body: {}, expect: 201 })
  record('POST /api/v1/conversations', 'ok')
  const convId = conv.body?.id
  if (!convId) throw new Error('新建会话的响应里没有 id')
  created.conversationIDs.push(convId)

  await call('GET', '/api/v1/conversations?limit=5', { expect: 200 })
  record('GET /api/v1/conversations', 'ok')

  await call('GET', `/api/v1/conversations/${convId}/messages?limit=5`, { expect: 200 })
  record('GET /api/v1/conversations/{id}/messages', 'ok')

  // ── 目录类 ──
  await call('GET', '/api/v1/tools', { expect: 200 })
  record('GET /api/v1/tools', 'ok')

  await call('GET', '/api/v1/agents', { expect: 200 })
  record('GET /api/v1/agents', 'ok')

  await call('GET', '/api/v1/usage', { expect: 200 })
  record('GET /api/v1/usage', 'ok')

  // ── 收尾：把这一轮造的数据删掉（顺便把两个删除端点也校验了）──
  await call('DELETE', `/api/v1/conversations/${convId}`, { expect: 204 })
  record('DELETE /api/v1/conversations/{id}', 'ok')

  await call('DELETE', `/api/v1/knowledge-bases/${kbId}`, { expect: 204 })
  record('DELETE /api/v1/knowledge-bases/{id}', 'ok')

  return results
}

/**
 * 没被这一轮覆盖的端点，以及为什么。
 *
 * 【为什么要把这份名单写在代码里】"覆盖不全"和"忘了覆盖"从结果上看不出
 * 区别；把理由写下来之后，issue 里那条"在 target 里写明覆盖范围与豁免理由"
 * 才算落地。
 */
const NOT_COVERED = [
  ['POST /api/v1/conversations/{id}/messages', '要打真实模型服务；且它是 SSE，契约对流式响应体只有占位描述'],
  ['GET /api/v1/conversations/{id}/events', '同上（SSE）'],
  ['POST /api/v1/knowledge-bases/{id}/documents', 'multipart 上传要一个真实文件与一个可用的 embedding 模型'],
  ['POST /api/v1/documents/{id}/reindex', '需要一份已上传的文档'],
  ['GET /api/v1/documents/{id}', '同上'],
  ['GET/POST /api/v1/providers', '写它会动到本机已配置的模型服务；只读那一侧改由"没有 provider 时仍是空列表"覆盖更有意义，但那个前提在这里不成立（开发机上通常配过）'],
  ['PATCH /api/v1/agents/{id}', '它要求先建一个 Agent，而**没有删除 Agent 的端点**——每跑一次都会在你的库里留一条。CI 上库是一次性的无所谓，本机会累积，所以这里刻意不测（响应形状与 GET /agents 里那个 Agent 基本重合）'],
  ['PATCH / DELETE /api/v1/models/{id}', '要动本机已配置的模型条目（删错了会让发消息立刻失败）。它们的业务规则（当前生效的删不掉、embedding 不能改名）在 internal/llm/model_admin_test.go 里测'],
  ['POST /api/v1/agents/{id}/runs', '要打真实模型服务（SSE）'],
  ['GET /api/v1/runs/{runId}/steps', '需要一条已存在的 run'],
  ['GET /api/v1/runs/{runId}/events', '同上（SSE）'],
  ['POST /api/v1/runs/{runId}/cancel', '同上'],
  ['POST /api/v1/runs/{runId}/resume', '同上'],
]

/**
 * 探一下数据库的 host:port 通不通。
 *
 * 【为什么要探，而不是让 api 自己去撞】api 连不上库时的日志是一句 pgx 的
 * 连接错误（`dial tcp 127.0.0.1:5432: connectex: ...`），而最常见的真实
 * 原因只有两个："忘了 make up"和"库在别的地址上"。一条 TCP 探测能把这两句
 * 话直接说出来。
 *
 * 【为什么用 TCP 而不是 pg_isready】那个客户端不在 PATH 里（本仓库已经因为
 * 同样的原因让 pg_dump 走容器）。这里只需要知道"端口通不通"，TCP 就够了。
 */
async function probeDatabase(url) {
  const { hostname, port } = new URL(url)
  return new Promise((resolve) => {
    const socket = net.connect({ host: hostname, port: Number(port || 5432) })
    const done = (okFlag) => {
      socket.destroy()
      resolve(okFlag)
    }
    socket.setTimeout(3000)
    socket.on('connect', () => done(true))
    socket.on('timeout', () => done(false))
    socket.on('error', () => done(false))
  })
}

async function main() {
  // ── 前置条件 ──
  //
  // 【为什么只探数据库，不要求 Docker】CI 的 integration job 里数据库是
  // workflow 起的 service container，本机是 Docker Desktop 里那个 compose
  // 容器——两者都只是"一个能连上的 host:port"。要求 Docker 会把这个脚本
  // 绑死在"库一定跑在本地 Docker 里"这个假设上。
  if (!(await probeDatabase(dbURL))) {
    const { hostname, port } = new URL(dbURL)
    fail(
      `连不上 PostgreSQL（${hostname}:${port || 5432}）——api 起不来。\n` +
        '  本机：在 Docker Desktop 的「容器」页里启动 congorag-postgres，或者跑 make up。\n' +
        '  CI：integration job 里的 service container 应该已经在监听了。',
    )
  }
  info(`数据库可达：${new URL(dbURL).host}`)

  step('编译 api')
  // 【Windows 上必须有 .exe 后缀】没有后缀的文件 Windows 不会当成可执行文件
  // ——`spawn` 报的是一个和真实原因无关的 "fetch failed"，因为进程压根没起来。
  const binName = process.platform === 'win32' ? 'api.exe' : 'api'
  const apiBin = join(mkdtempSync(join(tmpdir(), 'congorag-contract-')), binName)
  const build = spawnSync('go', ['build', '-o', apiBin, './apps/api'], {
    cwd: repoRoot,
    stdio: 'inherit',
    shell: process.platform === 'win32',
  })
  if (build.status !== 0) fail('go build ./apps/api 失败')
  ok('编译完成')

  let apiHandle = null
  let prismHandle = null
  let batteryErr = null
  let results = []
  try {
    step(`起 api（:${apiPort}）`)
    apiHandle = startProcess(apiBin, [], {
      env: {
        ...process.env,
        CONGORAG_DB_URL: dbURL,
        CONGORAG_LISTEN_ADDR: `127.0.0.1:${apiPort}`,
        CONGORAG_PORT: String(apiPort),
        CONGORAG_LOG_LEVEL: 'warn',
      },
    })
    try {
      await waitForHttp(`${apiBase}/healthz`, 30_000, 'api')
    } catch (err) {
      // 【起不来就把它的输出打出来】不然只剩一句 "fetch failed"，而真实原因
      // （库里没有迁移、端口被占、某个必需的环境变量没设）全在它自己的日志里。
      console.error('\n  api 的输出：')
      for (const line of apiHandle.lines.slice(-40)) console.error(`    ${line}`)
      throw err
    }
    ok(`api 就绪：${apiBase}`)

    step('起 prism proxy')
    // 【不要用 mock 模式】见文件头。这一行是整条链路上最关键的一处。
    // npx 是 .cmd，必须走 shell。
    prismHandle = startProcess('npx', [
      '--yes',
      '--no-fund',
      '--no-audit',
      PRISM,
      'proxy',
      'contracts/openapi.yaml',
      apiBase,
      '-p',
      String(prismPort),
      '--errors',
    ], { useShell: process.platform === 'win32' })
    await waitForHttp(`${prismBase}/healthz`, 90_000, 'prism（首次跑要下载它）')
    ok(`prism 就绪：${prismBase}`)

    // 【断言失败不立刻退出】先记下来，等 prism 的违规日志也拿到了一起报。
    // 一条"期望 200、实际 500"的断言如果没有 prism 那边的解释，排查要从
    // 头开始；两边一起看，通常第一眼就知道是哪一段对不上。
    try {
      results = await runBattery()
    } catch (err) {
      batteryErr = err
    }
  } finally {
    // 【顺序：先清理、再停进程】清理要通过 prism 打 api，两者都得还活着。
    await cleanupCreated(prismHandle)
    stopProcess(prismHandle)
    stopProcess(apiHandle)
    // 【清理失败绝不能盖住真正的错误】`finally` 里抛异常会把 try 里那个
    // 异常顶掉，报出来的就变成一句 "EPERM: unlink ..."，而真正失败的
    // 原因（契约违规、某个请求 500）一个字都看不到。Windows 上刚 taskkill
    // 完的二进制常常还锁着几毫秒，所以这里必须容错。
    try {
      rmSync(apiBin, { force: true })
    } catch {
      // 临时目录由操作系统回收，留一个几十 MB 的二进制不是问题。
    }
  }

  // prism 的校验违规写在它自己的日志里（形如 `[VALIDATOR]` 开头的行），
  // 而不是靠退出码——proxy 模式的退出码只表示"服务器有没有崩"。
  const violations = (prismHandle?.lines ?? []).filter(
    (l) => l.includes('[VALIDATOR]') || l.startsWith('✖') || l.includes('error: '),
  )

  step('结果')
  for (const r of results) info(`✓ ${r.name}`)
  for (const f of batteryFailures) console.error(`  ✗ ${f}`)
  // 【batteryErr 和 batteryFailures 是两回事】前者是"没有它就跑不下去"
  // （比如新建知识库没返回 id），后者是 --keep-going 攒下来的断言失败。
  if (batteryErr) console.error(`  ✗ ${batteryErr.message}`)

  step('Prism 校验')
  if (violations.length === 0) {
    ok('没有任何请求/响应违反契约')
  } else {
    console.error('')
    for (const v of violations) console.error(`  ${v}`)
    console.error('')
  }

  if (violations.length > 0 || batteryErr || batteryFailures.length > 0) {
    fail(
      `契约测试失败：${violations.length} 条 prism 违规、` +
        `${batteryFailures.length + (batteryErr ? 1 : 0)} 条断言失败（原始输出都在上面）`,
    )
  }

  step('没有覆盖到的端点')
  for (const [endpoint, why] of NOT_COVERED) info(`${endpoint} —— ${why}`)

  console.log('\n✓ 契约测试通过\n')
}

main().catch((err) => {
  if (keepGoing) console.error(err)
  fail(err?.message ?? String(err))
})
