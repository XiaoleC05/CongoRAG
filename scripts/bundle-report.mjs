#!/usr/bin/env node
/**
 * 前端产物体积报告 / 基线门禁（R1 核心性能基线）。
 *
 * 【为什么是一个脚本，而不是把数字写死在文档里】体积是会自己长回去的东西：
 * 一次「就多引一个库」、一次「这个页面顺手 import 一下」，首屏就涨几十 KB，
 * 而构建、类型检查、lint 全都不报错。把当场量出来的数字固化成基线、
 * 让 CI 每次构建后比一次，才有"涨了会被发现"这回事。
 *
 * 【为什么只用 Node 标准库】这个脚本要能在 CI 的 web job 里跑，那里不装
 * 任何额外依赖（连 Playwright 都没有）。解析 index.html 是正则、gzip 是
 * `node:zlib`、目录遍历是 `node:fs`——够用，且没有"报告工具自身也需要
 * 维护依赖"的问题。
 *
 * 【为什么直接读 index.html 判断"首屏"】Vite 会把入口 chunk 和它的静态
 * 依赖写进 index.html（script + modulepreload），并把样式表写成 link。
 * 这三类字节就是浏览器打开页面、渲染首屏之前必须下载的东西，比
 * "所有 assets 加起来"有意义得多——后者包含懒加载页面和 pdf worker。
 *
 * 【为什么比较时要用标准化后的 chunk 名】产物文件名带内容哈希
 * （`index-Wfn3WbQO.js`），每次构建都变。按原文件名比基线等于每次都
 * "这个 chunk 消失了、那个 chunk 是新的"。这里把末尾的 `-xxxxxxxx` 哈希
 * 剥掉，用逻辑名（index / ReaderPage / react）做键。
 *
 * 用法：
 *   node scripts/bundle-report.mjs                              # 打人类可读报告
 *   node scripts/bundle-report.mjs --json docs/perf/bundle-baseline.json
 *   node scripts/bundle-report.mjs --check                      # 与基线比，超预算退出 1
 *   node scripts/bundle-report.mjs --check --baseline <path>
 *
 * 【--check 的预算从哪来】首屏 gzip +4%、单个业务 chunk gzip +8%、
 * ReaderPage 与 pdf worker gzip +5%。取整数值而不是"零容忍"，是因为
 * 依赖小版本升级、一次正常的功能增量都会带来个位数百分比的变化；
 * 把阈值压到 1% 只会让门禁变成"随机红"，然后大家学会无视红色。
 */

import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import zlib from 'node:zlib'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '..')
const BUILD_DIR = path.join(ROOT, 'apps/api/web')
const DEFAULT_BASELINE = path.join(ROOT, 'docs/perf/bundle-baseline.json')

/** 预算（百分比）。见文件头"预算从哪来"。 */
const BUDGET = {
  initial: 4,
  businessChunk: 8,
  reader: 5,
  pdfWorker: 5,
}

/**
 * "业务 chunk"的判据：页面（*Page）、hooks（useXxx）、流式对话（streamChat），
 * 以及几个体积大到值得单独盯的业务组件。
 *
 * 【为什么不把 react / vendor / radix / lowlight 也算进来】它们的体积由
 * 依赖版本决定，一次正常升级涨 10% 是预期内的；盯着它们只会制造噪音。
 * 真正要拦的是"业务代码把某人拉进了首屏/某个页面 chunk"。
 */
const BUSINESS_CHUNK =
  /(?:Page|Dialog|Panel|Card|Bubble|Badge|Toolbar|Button)$|^use[A-Z][A-Za-z]*$|^streamChat$|^(?:AgentFormDialog|ToolCallCard|CopyButton|RunListToolbar|CitationBadge|MessageBubble|MarkdownContent|CodeBlock)$/

/** 千分位（报告里要能一眼看出量级）。 */
const fmt = (n) => n.toLocaleString('en-US')
/** 展示用路径：仓库内显示相对路径，仓库外（比如临时基线）显示绝对路径。 */
const showPath = (p) => {
  const rel = path.relative(ROOT, p)
  return rel.startsWith('..') ? p : rel
}
const kb = (n) => `${(n / 1024).toFixed(1)} KB`
const pct = (base, cur) => `${cur >= base ? '+' : ''}${(((cur - base) / base) * 100).toFixed(2)}%`

/**
 * 去掉文件名里的内容哈希，得到跨构建稳定的逻辑名。
 * `index-Wfn3WbQO.js` → `index`；`pdf.worker.min-CGwSHl62.js` → `pdf.worker.min`。
 */
function normalizeChunkName(fileName) {
  // 【长度写成 6–12 而不是死写 8】Vite / rolldown 换哈希长度时，死写 8 会让
  // 所有 key 对不上，门禁于是静默变宽（每次都比"基线里没有"）。范围比精确值
  // 更能抵抗工具升级；匹配不到时只剥掉 .js，至少 key 仍然稳定。
  return fileName.replace(/-[A-Za-z0-9_-]{6,12}\.js$/, '').replace(/\.js$/, '')
}

function gzipSize(buf) {
  return zlib.gzipSync(buf, { level: 9 }).length
}

function statFile(absPath) {
  const buf = fs.readFileSync(absPath)
  return { raw: buf.length, gzip: gzipSize(buf) }
}

/** 从 index.html 里取出入口脚本、modulepreload、样式表三类资源。 */
function parseIndexHtml(html) {
  const scriptSrc = html.match(/<script[^>]*\btype="module"[^>]*\bsrc="([^"]+)"/)
  const links = [...html.matchAll(/<link\b[^>]*>/g)].map((m) => m[0])

  const assetOf = (href) => path.join(BUILD_DIR, href.replace(/^\//, ''))
  const modulepreloads = []
  const stylesheets = []
  for (const tag of links) {
    const href = tag.match(/\bhref="([^"]+)"/)?.[1]
    if (!href) continue
    if (/\brel="modulepreload"/.test(tag)) modulepreloads.push(assetOf(href))
    else if (/\brel="stylesheet"/.test(tag)) stylesheets.push(assetOf(href))
  }

  if (!scriptSrc) {
    throw new Error('index.html 里找不到 <script type="module" src="...">：构建产物格式变了？')
  }
  return { entry: assetOf(scriptSrc[1]), modulepreloads, stylesheets }
}

/** 读 assets/ 下的全部文件（递归，当前是平铺的，但不假设这一点）。 */
function listAssetFiles() {
  const assetsDir = path.join(BUILD_DIR, 'assets')
  if (!fs.existsSync(assetsDir)) {
    throw new Error(`找不到 ${path.relative(ROOT, assetsDir)}：先跑 'pnpm --filter web build'`)
  }
  const out = []
  const walk = (dir) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const abs = path.join(dir, entry.name)
      if (entry.isDirectory()) walk(abs)
      else out.push({ abs, name: path.relative(assetsDir, abs).split(path.sep).join('/') })
    }
  }
  walk(assetsDir)
  return out
}

function readCommit() {
  try {
    return execFileSync('git', ['rev-parse', '--short', 'HEAD'], {
      cwd: ROOT,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim()
  } catch {
    // 不在 git 仓库里（或没装 git）时不该让报告跑不起来。
    return null
  }
}

/** 采集当前构建的完整体积画像。 */
function collect() {
  const indexPath = path.join(BUILD_DIR, 'index.html')
  if (!fs.existsSync(indexPath)) {
    throw new Error(`找不到 ${path.relative(ROOT, indexPath)}：先跑 'pnpm --filter web build'`)
  }
  const html = fs.readFileSync(indexPath, 'utf8')
  const { entry, modulepreloads, stylesheets } = parseIndexHtml(html)
  const assets = listAssetFiles()

  const byAbs = new Map(assets.map((a) => [a.abs, a]))
  const sizeOf = (abs) => {
    const file = byAbs.get(abs)
    if (!file) throw new Error(`index.html 引用了 assets/ 里不存在的文件：${path.basename(abs)}`)
    return { name: file.name, ...statFile(abs) }
  }

  const entryFile = sizeOf(entry)
  const preloadFiles = modulepreloads.map(sizeOf)
  const styleFiles = stylesheets.map(sizeOf)

  const initialJs = [entryFile, ...preloadFiles]
  const sum = (list) => list.reduce((acc, f) => ({ raw: acc.raw + f.raw, gzip: acc.gzip + f.gzip }), { raw: 0, gzip: 0 })
  const initialJsSum = sum(initialJs)
  const cssSum = sum(styleFiles)
  const initialSum = {
    raw: initialJsSum.raw + cssSum.raw,
    gzip: initialJsSum.gzip + cssSum.gzip,
  }

  // 全部 JS chunk 的尺寸（含 worker 与懒加载页面），按 raw 降序。
  const jsChunks = assets
    .filter((a) => a.name.endsWith('.js'))
    .map((a) => ({ name: a.name, key: normalizeChunkName(path.basename(a.name)), ...statFile(a.abs) }))
    .sort((a, b) => b.raw - a.raw)

  const pdfWorker = jsChunks.find((c) => c.key.startsWith('pdf.worker')) ?? null

  const total = sum(assets.map((a) => statFile(a.abs)))

  return {
    generatedAt: new Date().toISOString(),
    commit: readCommit(),
    buildDir: path.relative(ROOT, BUILD_DIR).split(path.sep).join('/'),
    initial: {
      raw: initialSum.raw,
      gzip: initialSum.gzip,
      js: initialJsSum,
      css: cssSum,
      files: initialJs.map((f) => f.name).concat(styleFiles.map((f) => f.name)),
    },
    entry: entryFile,
    chunks: Object.fromEntries(jsChunks.map((c) => [c.key, { raw: c.raw, gzip: c.gzip }])),
    topChunks: jsChunks.slice(0, 10).map((c) => ({ name: c.key, file: c.name, raw: c.raw, gzip: c.gzip })),
    pdfWorker: pdfWorker ? { name: pdfWorker.key, file: pdfWorker.name, raw: pdfWorker.raw, gzip: pdfWorker.gzip } : null,
    totals: { files: assets.length, raw: total.raw, gzip: total.gzip },
  }
}

function printReport(report) {
  const lines = []
  lines.push('CongoRAG 前端产物体积报告')
  lines.push(`构建目录：${report.buildDir}`)
  lines.push(`生成时间：${report.generatedAt}${report.commit ? `   提交：${report.commit}` : ''}`)
  lines.push('')
  lines.push('首屏（入口 script + modulepreload + stylesheet）')
  lines.push(`  raw ${fmt(report.initial.raw)} B   gzip ${fmt(report.initial.gzip)} B`)
  lines.push(`  ├ JS  ${fmt(report.initial.js.raw)} B / gzip ${fmt(report.initial.js.gzip)} B`)
  lines.push(`  └ CSS ${fmt(report.initial.css.raw)} B / gzip ${fmt(report.initial.css.gzip)} B`)
  lines.push('')
  lines.push(`入口 chunk  ${report.entry.name}  ${fmt(report.entry.raw)} B / gzip ${fmt(report.entry.gzip)} B`)
  lines.push('')
  lines.push('最大 chunk Top10（按 raw 降序）')
  report.topChunks.forEach((c, i) => {
    lines.push(`  ${String(i + 1).padStart(2)}. ${c.name.padEnd(28)} ${fmt(c.raw).padStart(12)} B / gzip ${fmt(c.gzip).padStart(9)} B  (${kb(c.raw)})`)
  })
  lines.push('')
  if (report.pdfWorker) {
    lines.push(`PDF worker  ${report.pdfWorker.name}  ${fmt(report.pdfWorker.raw)} B / gzip ${fmt(report.pdfWorker.gzip)} B`)
  } else {
    lines.push('PDF worker  未找到（阅读器 chunk 变了？）')
  }
  lines.push(`总计  ${report.totals.files} 个文件 / ${fmt(report.totals.raw)} B（gzip ${fmt(report.totals.gzip)} B）`)
  console.log(lines.join('\n'))
}

/** 与基线比较。返回 { violations, rows }，rows 用来打人类可读的对照表。 */
function checkBaseline(baseline, report) {
  const violations = []
  const rows = []

  const compare = (label, baseGzip, curGzip, budget) => {
    const limit = baseGzip * (1 + budget / 100)
    const ratio = (curGzip / baseGzip - 1) * 100
    const pass = curGzip <= limit
    rows.push({ label, baseGzip, curGzip, ratio, budget, pass })
    if (!pass) violations.push(`${label}：gzip ${fmt(baseGzip)} → ${fmt(curGzip)} B（${pct(baseGzip, curGzip)}，预算 +${budget}%）`)
  }

  // 首屏：涨一点点是正常的（新增一行文案都会变），所以预算按总字节算。
  compare('首屏 gzip', baseline.initial.gzip, report.initial.gzip, BUDGET.initial)

  // 单业务 chunk：只比基线里已有的、当前还在的业务 chunk。
  // ReaderPage 有自己的 +5% 档（见下面），这里跳过，否则同一个 chunk 会出两行、
  // 超预算时也会被报两次。
  //
  // 【基线里有、当前没有 → 判红，不再是跳过】"已改名/合并"听上去无害，但它
  // 是门禁的静默变宽路径：删掉一个页面、或把它从懒加载改成内联，基线里少一项，
  // 之后就永远不会再被检查。要删必须显式重跑 --json 刷新基线，让"删掉了一个
  // 业务 chunk"出现在 diff 里。
  for (const [key, base] of Object.entries(baseline.chunks)) {
    if (!BUSINESS_CHUNK.test(key) || key === 'ReaderPage' || key.startsWith('pdf.worker')) continue
    const cur = report.chunks[key]
    if (!cur) {
      violations.push(
        `业务 chunk ${key}：基线里有、当前构建里没有。确认是删除/合并后请重跑 --json 刷新基线`,
      )
      rows.push({ label: `业务 chunk ${key}`, missing: true })
      continue
    }
    compare(`业务 chunk ${key}`, base.gzip, cur.gzip, BUDGET.businessChunk)
  }

  // 新增业务 chunk（基线里没有）的不可能算"涨幅"，但一个 300KB 的新懒加载
  // 页面不该悄悄进仓库：超过 100KB raw 要求显式刷新基线，小的只提示。
  const NEW_CHUNK_RAW_LIMIT = 100 * 1024
  for (const [key, cur] of Object.entries(report.chunks)) {
    if (!BUSINESS_CHUNK.test(key) || key === 'ReaderPage' || key.startsWith('pdf.worker')) continue
    if (baseline.chunks[key]) continue
    if (cur.raw > NEW_CHUNK_RAW_LIMIT) {
      violations.push(
        `新增业务 chunk ${key}：raw ${fmt(cur.raw)} B > ${fmt(NEW_CHUNK_RAW_LIMIT)} B，确认体积后请刷新基线`,
      )
      rows.push({ label: `业务 chunk ${key}（新增）`, missing: true })
    } else {
      rows.push({ label: `业务 chunk ${key}（新增）`, note: `raw ${fmt(cur.raw)} B` })
    }
  }

  // 阅读器与 pdf worker 单独一档：它们是按需加载的"重页面"，涨 5% 就是几百 KB。
  for (const [label, key, budget] of [
    ['ReaderPage', 'ReaderPage', BUDGET.reader],
    ['pdf worker', 'pdf.worker.min', BUDGET.pdfWorker],
  ]) {
    const base = baseline.chunks[key]
    const cur = report.chunks[key]
    if (!base && !cur) continue
    if (base && !cur) {
      violations.push(`${label}：基线里有、当前构建里没有（chunk 被改名/删除了？确认后刷新基线）`)
      rows.push({ label, missing: true })
      continue
    }
    if (!base) {
      // 首次引入 ReaderPage / pdf worker 不判红：刷新基线即可。
      rows.push({ label, note: '基线里没有这一项，首次引入' })
      continue
    }
    compare(label, base.gzip, cur.gzip, budget)
  }

  return { violations, rows }
}

function printCheck(rows, violations, baselinePath, baseline) {
  console.log(`基线：${showPath(baselinePath)}（${baseline.generatedAt}${baseline.commit ? `，提交 ${baseline.commit}` : ''}）`)
  console.log('')
  for (const row of rows) {
    if (row.missing) {
      console.log(`  ✗ ${row.label.padEnd(30)} 缺失（见下方违规说明）`)
      continue
    }
    if (row.note) {
      console.log(`  · ${row.label.padEnd(30)} ${row.note}`)
      continue
    }
    const mark = row.pass ? '通过' : '超预算'
    console.log(
      `  ${row.pass ? '✓' : '✗'} ${row.label.padEnd(30)} gzip ${fmt(row.baseGzip).padStart(9)} → ${fmt(row.curGzip).padStart(9)} B  ${pct(row.baseGzip, row.curGzip).padStart(8)}  (预算 +${row.budget}%)  ${mark}`,
    )
  }
  console.log('')
  if (violations.length === 0) {
    console.log('结论：体积在预算内')
    return
  }
  console.log('结论：以下项目超出预算')
  for (const v of violations) console.log(`  - ${v}`)
}

function parseArgs(argv) {
  const opts = { json: null, check: false, baseline: DEFAULT_BASELINE }
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]
    if (arg === '--json') opts.json = argv[++i] ?? null
    else if (arg === '--check') opts.check = true
    else if (arg === '--baseline') opts.baseline = path.resolve(process.cwd(), argv[++i] ?? '')
    else if (arg === '--help' || arg === '-h') {
      console.log('用法：node scripts/bundle-report.mjs [--json <path>] [--check] [--baseline <path>]')
      process.exit(0)
    } else {
      throw new Error(`不认识的参数：${arg}`)
    }
  }
  return opts
}

function main() {
  const opts = parseArgs(process.argv.slice(2))
  const report = collect()
  printReport(report)

  let exitCode = 0
  if (opts.check) {
    if (!fs.existsSync(opts.baseline)) {
      // 首次运行：没有基线时写一份，这是**唯一**自动写基线的路径。
      // 有基线却悄悄覆盖，等于门禁永远绿——那比没有门禁更糟。
      fs.mkdirSync(path.dirname(opts.baseline), { recursive: true })
      fs.writeFileSync(opts.baseline, `${JSON.stringify(report, null, 2)}\n`)
      console.log(`\n基线不存在，已写入：${showPath(opts.baseline)}`)
    } else {
      const baseline = JSON.parse(fs.readFileSync(opts.baseline, 'utf8'))
      console.log('')
      const { rows, violations } = checkBaseline(baseline, report)
      printCheck(rows, violations, opts.baseline, baseline)
      if (violations.length > 0) exitCode = 1
    }
  }

  if (opts.json) {
    const jsonPath = path.resolve(process.cwd(), opts.json)
    fs.mkdirSync(path.dirname(jsonPath), { recursive: true })
    fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`)
    console.log(`\n已写入 JSON：${showPath(jsonPath)}`)
  }

  process.exit(exitCode)
}

try {
  main()
} catch (err) {
  console.error(`bundle-report 失败：${err instanceof Error ? err.message : String(err)}`)
  process.exit(2)
}
