#!/usr/bin/env node
/**
 * SSE 并发压测模拟器（R5）。
 *
 * 【它测什么】用一个内置的 mock SSE 服务，让 N 条并发连接同时接收 token 帧，
 * 测量：连接成功率、TTFB 分位、事件吞吐、接收字节率、错误数与进程 RSS 峰值。
 *
 * 【为什么 mock 目标有意义】mock 服务只按 docs/sse-protocol.md 的帧格式发字节，
 * 没有数据库、没有模型调用。所以它测的是**压测器 + SSE 分帧/解析路径**的容量
 * （也就是客户端侧的成本），不是后端真实吞吐。真实后端（DB + 模型）用
 * --target URL 指向即可，但那条路的延迟主要由检索 SQL 与模型首字决定，
 * 本工具只负责发连接与统计，不改变被压服务的语义。
 *
 * 【为什么要自己写分帧逻辑】与前端 web/src/lib/streamChat.ts 是同一套规则：
 * 累积字节、按空行切帧、逐行解析 id:/event:/data:、忽略 ":" 注释帧。
 * 用 EventSource 测不到这条路径（它把分帧藏在浏览器里）。
 *
 * 用法：
 *   node ops/loadtest/stream-load.mjs
 *   node ops/loadtest/stream-load.mjs --concurrency 25,50,100 --duration 10
 *   node ops/loadtest/stream-load.mjs --json loadtest.json --markdown loadtest.md
 *   node ops/loadtest/stream-load.mjs --target http://127.0.0.1:3210/api/v1/... --method POST --body '{"text":"hi"}' --header "Authorization: Bearer <token>"
 *
 * 参数：
 *   --concurrency <n[,n]>  并发连接数列表，默认 50（报告用 25,50,100 三档）
 *   --duration <秒>        每档持续时间，默认 10（下限 1）
 *   --interval <毫秒>      mock 服务发 token 的间隔，默认 50
 *   --target mock|<url>    默认 mock；给 URL 时直接连真实服务
 *   --method GET|POST      URL 模式的方法，默认 GET（真实聊天接口是 POST）
 *   --body <json>          URL 模式的请求体（POST 时用）
 *   --header "K: V"        额外请求头，可重复（鉴权头可选）
 *   --json <path>          机器可读 JSON（不给路径则打到 stdout）
 *   --markdown <path>      Markdown 表格片段
 *   --allow-errors         成功率 <100% 也按 0 退出（默认有失败连接时退 1）
 *
 * 退出码：0 全部成功；1 有连接失败（报告照写）；2 参数/环境错误。
 *
 * 【为什么只用 Node 标准库】CI 与评审机器上不该为压测再装 ws/autocannon：
 * http + 定时器 + async/await 足够，而且"零依赖"本身是可复现性的一部分。
 */

import http from 'node:http'
import os from 'node:os'
import fs from 'node:fs'
import path from 'node:path'

const DEFAULTS = {
  concurrency: '50',
  duration: 10,
  interval: 50,
  target: 'mock',
  method: 'GET',
  body: null,
  headers: [],
  jsonPath: null,
  markdownPath: null,
  allowErrors: false,
}

function parseArgs(argv) {
  const opts = { ...DEFAULTS }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    const next = () => argv[++i]
    if (a === '--concurrency') opts.concurrency = next() ?? DEFAULTS.concurrency
    else if (a === '--duration') opts.duration = Number(next() ?? DEFAULTS.duration)
    else if (a === '--interval') opts.interval = Number(next() ?? DEFAULTS.interval)
    else if (a === '--target') opts.target = next() ?? DEFAULTS.target
    else if (a === '--method') opts.method = (next() ?? 'GET').toUpperCase()
    else if (a === '--body') opts.body = next() ?? null
    else if (a === '--header') opts.headers.push(next() ?? '')
    else if (a === '--json') opts.jsonPath = path.resolve(process.cwd(), next() ?? '')
    else if (a === '--markdown') opts.markdownPath = path.resolve(process.cwd(), next() ?? '')
    else if (a === '--allow-errors') opts.allowErrors = true
    else if (a === '--help' || a === '-h') {
      console.log(
        'usage: node ops/loadtest/stream-load.mjs [--concurrency 25,50,100] [--duration 10] [--interval 50]\n' +
          '       [--target mock|URL] [--method GET|POST] [--body JSON] [--header "K: V"]...\n' +
          '       [--json out.json] [--markdown out.md] [--allow-errors]',
      )
      process.exit(0)
    } else {
      throw new Error('不认识的参数：' + a)
    }
  }
  const tiers = String(opts.concurrency)
    .split(',')
    .map((s) => Number(s.trim()))
    .filter((n) => Number.isFinite(n) && n > 0)
  if (tiers.length === 0) throw new Error('--concurrency 至少要有一个正整数')
  if (!(opts.duration >= 1)) throw new Error('--duration 必须 >= 1 秒')
  if (!(opts.interval >= 1)) throw new Error('--interval 必须 >= 1 毫秒')
  return { ...opts, tiers }
}

function parseHeader(text) {
  const idx = text.indexOf(':')
  if (idx <= 0) throw new Error('--header 格式应为 "K: V"：' + text)
  return [text.slice(0, idx).trim(), text.slice(idx + 1).trim()]
}

function percentile(sorted, p) {
  if (sorted.length === 0) return null
  if (p <= 0) return sorted[0]
  if (p >= 100) return sorted[sorted.length - 1]
  const rank = (p / 100) * (sorted.length - 1)
  const lo = Math.floor(rank)
  const hi = Math.ceil(rank)
  if (lo === hi) return sorted[lo]
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (rank - lo)
}

/** 与 web/src/lib/streamChat.ts 一致的 SSE 分帧器：缓冲、按空行切、逐行解析。 */
function createSSEParser(onEvent) {
  let buffer = ''
  return {
    push(chunk) {
      buffer += chunk.toString('utf8')
      for (;;) {
        const end = buffer.indexOf('\n\n')
        if (end === -1) return
        const frame = buffer.slice(0, end)
        buffer = buffer.slice(end + 2)
        let id = null
        let type = ''
        let data = ''
        for (const line of frame.split('\n')) {
          if (line.startsWith(':')) continue
          if (line.startsWith('id: ')) id = Number(line.slice(4))
          else if (line.startsWith('event: ')) type = line.slice(7)
          else if (line.startsWith('data: ')) data = line.slice(6)
        }
        if (!type) continue
        onEvent({ id, type, data })
      }
    },
  }
}

function startMockServer(intervalMs) {
  const state = { connections: 0, frames: 0, bytes: 0 }
  const server = http.createServer((req, res) => {
    if (!req.url || !req.url.startsWith('/stream')) {
      res.writeHead(404)
      res.end()
      return
    }
    state.connections += 1
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    })
    if (res.flushHeaders) res.flushHeaders()
    let seq = 0
    let closed = false
    const send = (frame) => {
      if (closed || res.writableEnded) return
      state.frames += 1
      state.bytes += Buffer.byteLength(frame)
      res.write(frame)
    }
    // 首帧立刻发：TTFB 才测得到"首字节"而不是等一个 interval。
    seq += 1
    send('id: ' + seq + '\nevent: token\ndata: {"type":"token","data":{"text":"首帧"}}\n\n')
    const timer = setInterval(() => {
      seq += 1
      send('id: ' + seq + '\nevent: token\ndata: {"type":"token","data":{"text":"第 ' + seq + ' 个 token"}}\n\n')
    }, intervalMs)
    req.on('close', () => {
      closed = true
      clearInterval(timer)
    })
    res.on('close', () => {
      closed = true
      clearInterval(timer)
    })
  })
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve({ server, state, port: server.address().port }))
  })
}

/** 发起一条连接并读取直到 durationMs 结束；返回该连接的结果。 */
function runOneClient(url, opts, durationMs, headers) {
  return new Promise((resolve) => {
    const startedAt = process.hrtime.bigint()
    let ttfbMs = null
    let events = 0
    let bytes = 0
    let statusCode = 0
    let error = null
    let ended = false
    let aborted = false

    const finish = () => {
      if (ended) return
      ended = true
      try {
        req.destroy()
      } catch {
        /* 已经关了 */
      }
      resolve({
        ok: !error && statusCode > 0 && statusCode < 400 && events > 0,
        statusCode,
        ttfbMs,
        events,
        bytes,
        error: error ? String(error.message ?? error) : aborted ? 'aborted by harness (expected)' : null,
      })
    }

    const parser = createSSEParser(() => {
      events += 1
      if (ttfbMs === null) ttfbMs = Number(process.hrtime.bigint() - startedAt) / 1e6
    })

    const req = http.request(
      url,
      {
        method: opts.method,
        headers: {
          Accept: 'text/event-stream',
          ...(opts.method === 'POST' && opts.body
            ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(opts.body) }
            : {}),
          ...headers,
        },
      },
      (res) => {
        statusCode = res.statusCode ?? 0
        if (statusCode >= 400) {
          error = new Error('HTTP ' + statusCode)
          res.resume()
          finish()
          return
        }
        res.on('data', (chunk) => {
          bytes += chunk.length
          parser.push(chunk)
        })
        res.on('error', (err) => {
          error = err
          finish()
        })
      },
    )
    req.on('error', (err) => {
      error = err
      finish()
    })
    if (opts.method === 'POST' && opts.body) req.write(opts.body)
    req.end()

    setTimeout(() => {
      aborted = true
      finish()
    }, durationMs)
  })
}

async function runTier(tier, opts, headers) {
  const requested = tier
  const durationMs = opts.duration * 1000
  const results = []
  let rssPeak = 0
  const rssTimer = setInterval(() => {
    rssPeak = Math.max(rssPeak, process.memoryUsage().rss)
  }, 100)

  const startedAt = process.hrtime.bigint()
  const tasks = []
  for (let i = 0; i < requested; i++) tasks.push(runOneClient(opts.targetResult.url, opts, durationMs, headers))
  const settled = await Promise.all(tasks)
  clearInterval(rssTimer)
  const elapsedMs = Number(process.hrtime.bigint() - startedAt) / 1e6
  results.push(...settled)

  const ok = results.filter((r) => r.ok)
  const ttfb = ok.map((r) => r.ttfbMs).filter((v) => v !== null).sort((a, b) => a - b)
  const events = results.reduce((acc, r) => acc + r.events, 0)
  const bytes = results.reduce((acc, r) => acc + r.bytes, 0)
  const errors = results.filter((r) => !r.ok)
  return {
    concurrency: tier,
    durationSec: opts.duration,
    intervalMs: opts.interval,
    requested,
    succeeded: ok.length,
    failed: requested - ok.length,
    successRate: requested === 0 ? 0 : ok.length / requested,
    ttfbMs: {
      p50: percentile(ttfb, 50),
      p95: percentile(ttfb, 95),
      p99: percentile(ttfb, 99),
      max: ttfb.length ? ttfb[ttfb.length - 1] : null,
    },
    events,
    bytes,
    elapsedSec: elapsedMs / 1000,
    eventsPerSec: elapsedMs > 0 ? events / (elapsedMs / 1000) : 0,
    bytesPerSec: elapsedMs > 0 ? bytes / (elapsedMs / 1000) : 0,
    rssPeakMB: Math.round((rssPeak / 1024 / 1024) * 10) / 10,
    errors: errors.slice(0, 5).map((r) => ({ statusCode: r.statusCode, error: r.error })),
    errorCount: errors.length,
  }
}

function fmt(n, digits = 0) {
  if (n === null || n === undefined || Number.isNaN(n)) return '-'
  return Number(n).toFixed(digits)
}

function renderMarkdown(report) {
  const lines = []
  lines.push('| 并发连接 | 成功率 | TTFB p50 | TTFB p95 | TTFB p99 | events/s | 接收字节/s | 错误数 | RSS 峰值 |')
  lines.push('| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |')
  for (const t of report.tiers) {
    lines.push(
      '| ' +
        t.concurrency +
        ' | ' +
        (t.successRate * 100).toFixed(1) +
        '% | ' +
        fmt(t.ttfbMs.p50, 1) +
        ' ms | ' +
        fmt(t.ttfbMs.p95, 1) +
        ' ms | ' +
        fmt(t.ttfbMs.p99, 1) +
        ' ms | ' +
        fmt(t.eventsPerSec, 1) +
        ' | ' +
        fmt(t.bytesPerSec / 1024 / 1024, 2) +
        ' MB/s | ' +
        t.errorCount +
        ' | ' +
        fmt(t.rssPeakMB, 1) +
        ' MB |',
    )
  }
  return lines.join('\n')
}

function renderConsole(report) {
  const lines = []
  lines.push('SSE loadtest — target=' + report.target.mode + (report.target.url ? ' ' + report.target.url : ''))
  lines.push(
    'tiers: ' +
      report.tiers.map((t) => t.concurrency).join(' / ') +
      ' connections, duration=' +
      report.durationSec +
      's each, token interval=' +
      report.intervalMs +
      'ms',
  )
  lines.push('')
  lines.push(renderMarkdown(report))
  return lines.join('\n')
}

function writeJson(report, jsonPath) {
  const text = JSON.stringify(report, null, 2)
  if (jsonPath) {
    fs.mkdirSync(path.dirname(jsonPath), { recursive: true })
    fs.writeFileSync(jsonPath, text + '\n')
    console.error('已写入 JSON：' + jsonPath)
  } else {
    console.log(text)
  }
}

function writeMarkdown(report, mdPath) {
  const body = renderMarkdown(report)
  if (mdPath) {
    fs.mkdirSync(path.dirname(mdPath), { recursive: true })
    fs.writeFileSync(mdPath, body + '\n')
    console.error('已写入 Markdown：' + mdPath)
  }
  console.log(body)
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  const headers = Object.fromEntries(opts.headers.map(parseHeader))
  const report = {
    tool: 'ops/loadtest/stream-load.mjs',
    generatedAt: new Date().toISOString(),
    environment: {
      node: process.version,
      platform: process.platform,
      arch: process.arch,
      cpus: os.cpus().length,
      totalMemGB: Math.round((os.totalmem() / 1024 ** 3) * 10) / 10,
    },
    durationSec: opts.duration,
    intervalMs: opts.interval,
    target: { mode: opts.target === 'mock' ? 'mock' : 'url', url: null },
    tiers: [],
  }

  let mock = null
  let baseUrl
  if (opts.target === 'mock') {
    mock = await startMockServer(opts.interval)
    baseUrl = 'http://127.0.0.1:' + mock.port + '/stream'
    report.target.url = baseUrl
    report.target.mockIntervalMs = opts.interval
  } else {
    baseUrl = opts.target
    report.target.url = baseUrl
    report.target.method = opts.method
    report.target.headers = Object.keys(headers)
  }

  try {
    for (const tier of opts.tiers) {
      console.error('running tier concurrency=' + tier + ' duration=' + opts.duration + 's ...')
      const result = await runTier(tier, { ...opts, targetResult: { url: baseUrl } }, headers)
      report.tiers.push(result)
    }
  } finally {
    if (mock) mock.server.close()
  }

  if (opts.jsonPath) writeJson(report, opts.jsonPath)
  if (opts.markdownPath) writeMarkdown(report, opts.markdownPath)
  else {
    console.log(renderConsole(report))
  }

  const failures = report.tiers.reduce((acc, t) => acc + t.errorCount, 0)
  if (failures > 0 && !opts.allowErrors) {
    console.error('有 ' + failures + ' 条连接失败（可用 --allow-errors 放宽退出码）')
    process.exit(1)
  }
}

main().catch((err) => {
  console.error('loadtest 失败：' + (err && err.stack ? err.stack : String(err)))
  process.exit(2)
})
