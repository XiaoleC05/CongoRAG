#!/usr/bin/env node
/**
 * 纯函数基准回归门禁（R5）。
 *
 * 回答的问题：**这次改动有没有把某条纯路径的常数改坏。**
 *
 * 数据源是 @go test -run "^$" -bench . -benchmem -count=3 ./internal/...@：
 * 只跑 Benchmark、不跑 Test，不碰数据库、不联网、不调模型。脚本把每条
 * benchmark 的三次采样取中位数，与 @benchmarks/baseline.json@ 比较。
 * 基准的公开说明与一次真实优化见 docs/performance.md。
 *
 * ── 判定（为什么这么定）─────────────────────────────────────
 *
 * · allocs/op **上涨一次就失败**：分配次数由代码决定、与机器无关，是最稳的
 *   回归信号（v6.2.0 去重优化的收益主要落在这一列）。但个别 benchmark 的分配
 *   次数本身会随 map 迭代顺序抖动（visualize/Build 实测 29,231~29,234），所以
 *   精确比较用「本次样本 min vs 基线样本 max」：确定性基准 min==max，等价于
 *   严格判据；抖动基准只吸收基线已经观测到的抖动；
 * · B/op 超过 +5% 失败：分配字节数基本确定，留一点 map/slice 容量余量；
 * · ns/op 只在 **超过 +30% 且绝对增量 >200ns** 时失败，否则只告警；
 *   比较用"三次采样里的最优值"而不是中位数：共享机器上偶发抢占会把一两次采样
 *   整体抬高，用中位数会把噪声判成回归；
 *   共享 runner / 本机其它进程会让 ns 抖 ±20%，阈值压到 10% 只会得到一条
 *   随机翻红的流水线，而随机红会训练人忽略红色。绝对下限 200ns 是为了保护
 *   20ns 量级的小函数不被 6ns 噪声判红；
 * · 机器/工具链与基线不一致（换机器、升级 Go）时，ns 判定降级为提示，
 *   allocs/B 仍然严格——它们在同一 GOOS/GOARCH/Go 版本下是确定量。
 *
 * ── 退出码 ────────────────────────────────────────────────
 *   0 = 通过（允许有 warning）
 *   1 = 有回归（allocs/B/ns 的失败判据至少一条成立）
 *   2 = 环境或解析错误（go 不在 PATH、编译失败、超时、基线缺失/损坏）
 *
 * 用法：
 *   node scripts/bench-gate.mjs                      # 跑基准并比较
 *   node scripts/bench-gate.mjs --write              # 重写基线（换机器/升级 Go 后）
 *   node scripts/bench-gate.mjs --json               # 机器可读结果打到 stdout
 *   node scripts/bench-gate.mjs --json out.json      # 机器可读结果写文件
 *   node scripts/bench-gate.mjs --current bench.txt  # 用已有输出比较（调试/复现）
 *   node scripts/bench-gate.mjs --save-current b.txt # 顺手保存本次原始输出
 *   node scripts/bench-gate.mjs --baseline other.json
 */

import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '..')
const DEFAULT_BASELINE = path.join(ROOT, 'benchmarks', 'baseline.json')
const BENCH_PACKAGES = './internal/...'
const BENCH_COUNT = 3
const RUN_TIMEOUT_MS = 10 * 60 * 1000

/** 默认阈值；基线文件里可以覆盖（改阈值要出现在 baseline 的 diff 里）。 */
const DEFAULT_THRESHOLDS = {
  allocsDelta: 0, // allocs/op 允许的绝对增量：0 = 一次都不许涨
  bytesPct: 5, // B/op 允许的相对增量（%）
  nsPct: 30, // ns/op 判红的相对增量（%）
  nsAbsFloor: 200, // ns/op 判红的绝对增量下限（ns）
  nsWarnPct: 15, // ns/op 只告警的相对增量（%）
}

function parseArgs(argv) {
  const opts = {
    write: false,
    json: false,
    jsonPath: null,
    baseline: DEFAULT_BASELINE,
    currentFile: null,
    saveCurrent: null,
  }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--write') opts.write = true
    else if (a === '--json') {
      opts.json = true
      if (argv[i + 1] && !argv[i + 1].startsWith('-')) opts.jsonPath = path.resolve(process.cwd(), argv[++i])
    } else if (a === '--baseline') opts.baseline = path.resolve(process.cwd(), argv[++i] ?? '')
    else if (a === '--current') opts.currentFile = path.resolve(process.cwd(), argv[++i] ?? '')
    else if (a === '--save-current') opts.saveCurrent = path.resolve(process.cwd(), argv[++i] ?? '')
    else if (a === '--help' || a === '-h') {
      console.log(
        '用法：node scripts/bench-gate.mjs [--write] [--json [path]] [--current file] [--baseline file] [--save-current file]\n' +
          '  --write         用本次结果重写基线（换机器 / 升级 Go 后）\n' +
          '  --json [path]   输出机器可读结果（不写 path 则打到 stdout）\n' +
          '  --current file  用已有的 go test -bench 输出比较，不重新跑\n' +
          '  --baseline file 指定基线路径\n' +
          '  --save-current  把本次原始输出保存下来（便于复现）',
      )
      process.exit(0)
    } else {
      console.error('不认识的参数：' + a)
      process.exit(2)
    }
  }
  return opts
}

function fail(message, extra) {
  console.error('BENCH GATE: ENV ERROR')
  console.error(message)
  if (extra) console.error(extra)
  process.exit(2)
}

/** 跑 go test 的基准，返回原始输出文本。 */
function runBenchmarks() {
  const args = ['test', '-run', '^$', '-bench', '.', '-benchmem', '-count', String(BENCH_COUNT), BENCH_PACKAGES]
  const started = Date.now()
  const res = spawnSync('go', args, {
    cwd: ROOT,
    encoding: 'utf8',
    timeout: RUN_TIMEOUT_MS,
    maxBuffer: 64 * 1024 * 1024,
  })
  const elapsedMs = Date.now() - started
  if (res.error) fail('执行 go test 失败：' + res.error.message)
  if (res.status !== 0) fail('go test 退出码 ' + res.status + '（基准没跑完）', ((res.stdout ?? '') + (res.stderr ?? '')).slice(-4000))
  const output = (res.stdout ?? '') + (res.stderr ?? '')
  if (!/^Benchmark/m.test(output)) fail('没有解析到任何 Benchmark 行：命令输出格式变了？', output.slice(-3000))
  console.error('> 基准完成：' + (elapsedMs / 1000).toFixed(1) + 's')
  return output
}

/** 解析 go test -bench 输出；同名 benchmark 的多行按 name 归组（-count=3 即三行）。 */
function parseBenchOutput(text) {
  // 名字后缀 -N 是 GOMAXPROCS；-benchmem 下 B/op 与 allocs/op 一定在，但保留可选匹配，
  // 避免将来命令少了 -benchmem 时整个脚本直接报解析错误。
  const line =
    /^(Benchmark\S+?)(?:-(\d+))?\s+(\d+)\s+([\d.]+)\s+ns\/op(?:\s+[\d.]+\s+MB\/s)?(?:\s+(\d+)\s+B\/op)?(?:\s+(\d+)\s+allocs\/op)?/
  const byName = new Map()
  let gomaxprocs = null
  for (const rawLine of text.split(/\r?\n/)) {
    const m = line.exec(rawLine.trim())
    if (!m) continue
    if (m[2]) gomaxprocs = Number(m[2])
    const name = m[1]
    if (!byName.has(name)) byName.set(name, { ns: [], bytes: [], allocs: [], iterations: [] })
    const item = byName.get(name)
    item.iterations.push(Number(m[3]))
    item.ns.push(Number(m[4]))
    if (m[5] != null) item.bytes.push(Number(m[5]))
    if (m[6] != null) item.allocs.push(Number(m[6]))
  }
  if (byName.size === 0) fail('Benchmark 行解析失败：没有一条匹配到格式')
  return { benchmarks: byName, gomaxprocs }
}

function median(list) {
  if (!list || list.length === 0) return null
  const sorted = [...list].sort((a, b) => a - b)
  const n = sorted.length
  return n % 2 === 1 ? sorted[(n - 1) / 2] : (sorted[n / 2 - 1] + sorted[n / 2]) / 2
}

function metric(list) {
  // 存 min/max 的原因：有些 benchmark 的分配次数本身就会随 map 迭代顺序抖动
  // （visualize/Build 实测 29,231~29,234 allocs 之间跳），只存中位数会让门禁
  // 在"没有回归"时随机翻红。比较时用"本次样本 min vs 基线样本 max"，
  // 确定性 benchmark（min==max）仍然等价于"上涨一次就失败"。
  return { median: median(list), min: list.length ? Math.min(...list) : null, max: list.length ? Math.max(...list) : null, samples: list }
}

function probeToolchain(gomaxprocs) {
  const run = (cmd, args) => spawnSync(cmd, args, { cwd: ROOT, encoding: 'utf8' }).stdout?.trim() ?? ''
  const goVersion = (run('go', ['version']).replace(/^go version\s+/, '').split(' ')[0] ?? 'unknown')
  return {
    generatedAt: new Date().toISOString(),
    gitHead: run('git', ['rev-parse', '--short', 'HEAD']) || null,
    goVersion,
    goos: run('go', ['env', 'GOOS']) || process.platform,
    goarch: run('go', ['env', 'GOARCH']) || process.arch,
    gomaxprocs: gomaxprocs ?? (Number(run('go', ['env', 'GOMAXPROCS'])) || os.cpus().length),
    machine: {
      platform: os.platform(),
      release: os.release(),
      cpus: os.cpus().length,
      totalMemGB: Math.round((os.totalmem() / 1024 ** 3) * 10) / 10,
      node: process.version,
    },
  }
}

function buildBaseline(parsed) {
  const meta = probeToolchain(parsed.gomaxprocs)
  const benchmarks = {}
  for (const name of [...parsed.benchmarks.keys()].sort()) {
    const item = parsed.benchmarks.get(name)
    benchmarks[name] = {
      nsPerOp: metric(item.ns),
      bytesPerOp: metric(item.bytes),
      allocsPerOp: metric(item.allocs),
      iterations: metric(item.iterations),
    }
  }
  return {
    schemaVersion: 1,
    kind: 'go-benchmarks',
    ...meta,
    count: BENCH_COUNT,
    packages: BENCH_PACKAGES,
    command: 'go test -run "^$" -bench . -benchmem -count=' + BENCH_COUNT + ' ' + BENCH_PACKAGES,
    statistic: 'median',
    thresholds: { ...DEFAULT_THRESHOLDS },
    benchmarks,
  }
}

/** 比较逻辑：纯函数，返回表格行 / 违规 / 警告。 */
function compare(baseline, parsed, currentMeta) {
  const thresholds = { ...DEFAULT_THRESHOLDS, ...(baseline.thresholds ?? {}) }
  const rows = []
  const violations = []
  const warnings = []

  const envMismatch = []
  if (baseline.goos && currentMeta.goos !== baseline.goos) envMismatch.push('GOOS ' + baseline.goos + ' -> ' + currentMeta.goos)
  if (baseline.goarch && currentMeta.goarch !== baseline.goarch) envMismatch.push('GOARCH ' + baseline.goarch + ' -> ' + currentMeta.goarch)
  if (baseline.goVersion && currentMeta.goVersion !== baseline.goVersion) envMismatch.push('Go ' + baseline.goVersion + ' -> ' + currentMeta.goVersion)
  if (baseline.gomaxprocs && currentMeta.gomaxprocs !== baseline.gomaxprocs) envMismatch.push('GOMAXPROCS ' + baseline.gomaxprocs + ' -> ' + currentMeta.gomaxprocs)
  const envDiffers = envMismatch.length > 0
  if (envDiffers) {
    warnings.push('基线环境与当前不一致（' + envMismatch.join('；') + '）：ns/op 降级为提示，allocs/B 仍严格；换机器或升级 Go 后应重跑 --write。')
  }

  const names = [...new Set([...Object.keys(baseline.benchmarks ?? {}), ...parsed.benchmarks.keys()])].sort()
  for (const name of names) {
    const base = baseline.benchmarks?.[name]
    const cur = parsed.benchmarks.get(name)
    const row = { name, status: 'ok', fields: [] }
    rows.push(row)
    if (!base) {
      row.status = 'warn'
      warnings.push('新增 benchmark ' + name + '：基线里没有它，确认后重跑 --write 纳入门禁。')
      continue
    }
    if (!cur) {
      row.status = 'warn'
      warnings.push('基线里的 benchmark ' + name + ' 本次没有产出：被删除/改名还是被 build tag 关掉？确认后重跑 --write。')
      continue
    }

    // ns 用"三次采样里的最优值"比较：共享机器上偶发的构建/测试抢占会把某几次
    // 采样整体抬高（实测 Verify 同一次 run 内 9.2ms → 13ms），中位数也会被带着走；
    // 真正的回归会让三次采样一起变慢，最优值同样会变慢。基线里同时存了 median
    // 与 min，判定用 min，展示仍能看到中位数位置的量级。
    const baseNs = base.nsPerOp?.min ?? base.nsPerOp?.median ?? null
    const curNs = cur.ns.length ? Math.min(...cur.ns) : null
    // 分配类指标用"基线观测上界 vs 本次观测下界"：
    //   · 确定性 benchmark：基线 min==max、本次 min==样本值 → 等价于严格判据；
    //   · 有抖动的 benchmark：吸收基线已有的抖动，但超过历史上界仍判红。
    const baseB = base.bytesPerOp?.max ?? base.bytesPerOp?.median ?? null
    const curB = cur.bytes.length ? Math.min(...cur.bytes) : null
    const baseA = base.allocsPerOp?.max ?? base.allocsPerOp?.median ?? null
    const curA = cur.allocs.length ? Math.min(...cur.allocs) : null

    if (baseA != null && curA != null) {
      const delta = curA - baseA
      const ok = delta <= thresholds.allocsDelta
      row.fields.push({ key: 'allocs/op', base: baseA, cur: curA, delta, status: ok ? 'ok' : 'fail' })
      if (!ok) {
        row.status = 'fail'
        violations.push(name + ' allocs/op 上涨：基线上界 ' + baseA + ' -> 本次下界 ' + curA + '（+' + delta + '，阈值 +' + thresholds.allocsDelta + '）')
      }
    }

    if (baseB != null && curB != null && baseB > 0) {
      const pct = ((curB - baseB) / baseB) * 100
      const ok = pct <= thresholds.bytesPct
      row.fields.push({ key: 'B/op', base: baseB, cur: curB, pct, status: ok ? 'ok' : 'fail' })
      if (!ok) {
        row.status = 'fail'
        violations.push(name + ' B/op 上涨：基线上界 ' + baseB + ' -> 本次下界 ' + curB + '（+' + pct.toFixed(1) + '%，阈值 +' + thresholds.bytesPct + '%）')
      }
    }

    if (baseNs != null && curNs != null) {
      const pct = baseNs > 0 ? ((curNs - baseNs) / baseNs) * 100 : 0
      const abs = curNs - baseNs
      const hardFail = pct > thresholds.nsPct && abs > thresholds.nsAbsFloor
      const warn = !hardFail && pct > thresholds.nsWarnPct
      let status = warn ? 'warn' : 'ok'
      if (hardFail) status = envDiffers ? 'warn' : 'fail'
      row.fields.push({ key: 'ns/op', base: baseNs, cur: curNs, pct, abs, status })
      if (hardFail && envDiffers) {
        warnings.push(name + ' ns/op 上涨 ' + pct.toFixed(1) + '%（+' + Math.round(abs) + 'ns），但基线环境不同：只提示、不判红。')
      } else if (hardFail) {
        row.status = 'fail'
        violations.push(name + ' ns/op 上涨：' + Math.round(baseNs) + ' -> ' + Math.round(curNs) + '（+' + pct.toFixed(1) + '%，+' + Math.round(abs) + 'ns；阈值 +' + thresholds.nsPct + '% 且 >' + thresholds.nsAbsFloor + 'ns）')
      } else if (warn) {
        if (row.status !== 'fail') row.status = 'warn'
        warnings.push(name + ' ns/op 上涨 ' + pct.toFixed(1) + '%（+' + Math.round(abs) + 'ns），未触发判红阈值，仅提示。')
      }
    }
  }

  return { rows, violations, warnings, envDiffers, envMismatch, thresholds }
}

function pad(s, n) {
  s = String(s)
  return s.length >= n ? s.slice(0, n - 1) + '…' : s + ' '.repeat(n - s.length)
}

function formatTable(rows) {
  const lines = [pad('benchmark', 46) + pad('ns/op', 24) + pad('B/op', 20) + pad('allocs/op', 18) + 'status']
  for (const row of rows) {
    const get = (key) => row.fields.find((f) => f.key === key)
    const ns = get('ns/op')
    const bytes = get('B/op')
    const allocs = get('allocs/op')
    const nsCell = ns
      ? pad(Math.round(ns.base) + ' -> ' + Math.round(ns.cur) + ' (' + (ns.pct >= 0 ? '+' : '') + ns.pct.toFixed(0) + '%)', 24)
      : pad('-', 24)
    const bCell = bytes ? pad(bytes.base + ' -> ' + bytes.cur + ' (' + (bytes.pct >= 0 ? '+' : '') + bytes.pct.toFixed(0) + '%)', 20) : pad('-', 20)
    const aCell = allocs ? pad(allocs.base + ' -> ' + allocs.cur + (allocs.delta ? ' (+' + allocs.delta + ')' : ''), 18) : pad('-', 18)
    const mark = row.status === 'fail' ? 'FAIL' : row.status === 'warn' ? 'WARN' : 'PASS'
    lines.push(pad(row.name, 46) + nsCell + bCell + aCell + mark)
  }
  return lines.join('\n')
}

function printResult(result, opts) {
  console.log('CongoRAG 基准回归门禁（' + BENCH_PACKAGES + '，count=' + BENCH_COUNT + '，statistic=median）')
  console.log('基线：' + path.relative(ROOT, opts.baseline))
  console.log('')
  console.log('判定口径：allocs/B/op 用「本次样本最小值 vs 基线样本最大值」（确定性基准等价于严格相等；抖动基准容忍基线已观测的抖动）；ns/op 用「本次样本最小值 vs 基线样本最小值」（被抢占抬高的采样不判红，真实回归会让所有样本一起变慢）。')
  console.log('')
  console.log(formatTable(result.rows))
  console.log('')
  if (result.envDiffers) console.log('环境差异：' + result.envMismatch.join('；'))
  for (const w of result.warnings) console.log('WARN  ' + w)
  for (const v of result.violations) console.log('FAIL  ' + v)
  console.log('')
  if (result.violations.length === 0) {
    console.log('BENCH GATE: PASS' + (result.warnings.length ? '（' + result.warnings.length + ' 条警告）' : ''))
  } else {
    console.log('BENCH GATE: FAIL（' + result.violations.length + ' 条回归）')
  }
}

function emitJson(payload, pathOrNull) {
  const text = JSON.stringify(payload, null, 2)
  if (pathOrNull) {
    fs.mkdirSync(path.dirname(pathOrNull), { recursive: true })
    fs.writeFileSync(pathOrNull, text + '\n')
    console.error('已写入 JSON：' + pathOrNull)
  } else {
    console.log(text)
  }
}

function main() {
  const opts = parseArgs(process.argv.slice(2))

  let rawOutput
  if (opts.currentFile) {
    if (!fs.existsSync(opts.currentFile)) fail('--current 文件不存在：' + opts.currentFile)
    rawOutput = fs.readFileSync(opts.currentFile, 'utf8')
  } else {
    rawOutput = runBenchmarks()
  }
  if (opts.saveCurrent) {
    fs.mkdirSync(path.dirname(opts.saveCurrent), { recursive: true })
    fs.writeFileSync(opts.saveCurrent, rawOutput)
    console.error('已保存本次基准输出：' + opts.saveCurrent)
  }

  const parsed = parseBenchOutput(rawOutput)
  const currentMeta = probeToolchain(parsed.gomaxprocs)

  if (opts.write) {
    const baseline = buildBaseline(parsed)
    fs.mkdirSync(path.dirname(opts.baseline), { recursive: true })
    fs.writeFileSync(opts.baseline, JSON.stringify(baseline, null, 2) + '\n')
    console.log('BENCH GATE: WROTE baseline（' + Object.keys(baseline.benchmarks).length + ' 个 benchmark）')
    console.log('  -> ' + opts.baseline)
    console.log('     commit=' + baseline.gitHead + ' go=' + baseline.goVersion + ' ' + baseline.goos + '/' + baseline.goarch + ' gomaxprocs=' + baseline.gomaxprocs)
    return
  }

  if (!fs.existsSync(opts.baseline)) {
    fail('基线文件不存在：' + opts.baseline + '；首次使用请先跑 node scripts/bench-gate.mjs --write')
  }
  let baseline
  try {
    baseline = JSON.parse(fs.readFileSync(opts.baseline, 'utf8'))
  } catch (err) {
    fail('基线文件不是合法 JSON：' + String(err && err.message ? err.message : err))
  }
  if (!baseline.benchmarks || typeof baseline.benchmarks !== 'object') fail('基线缺少 benchmarks 对象')

  const result = compare(baseline, parsed, currentMeta)
  printResult(result, opts)

  if (opts.json) {
    emitJson(
      {
        schemaVersion: 1,
        generatedAt: new Date().toISOString(),
        baseline: path.relative(ROOT, opts.baseline),
        baselineGeneratedAt: baseline.generatedAt ?? null,
        baselineGitHead: baseline.gitHead ?? null,
        exitCode: result.violations.length > 0 ? 1 : 0,
        envDiffers: result.envDiffers,
        envMismatch: result.envMismatch,
        thresholds: result.thresholds,
        rows: result.rows,
        violations: result.violations,
        warnings: result.warnings,
      },
      opts.jsonPath,
    )
  }

  process.exit(result.violations.length > 0 ? 1 : 0)
}

try {
  main()
} catch (err) {
  console.error('BENCH GATE: ENV ERROR')
  console.error(err instanceof Error ? (err.stack ?? err.message) : String(err))
  process.exit(2)
}
