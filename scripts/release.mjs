#!/usr/bin/env node
/**
 * 发布脚本（issue #52）。
 *
 * 一条命令做完：打 annotated tag（消息 = CHANGELOG 对应小节）→ 推 tag →
 * 建或更新 GitHub Release（正文 = 同一段 + compare 链接）。
 *
 * 【为什么是 .mjs】仓库根没有 package.json，`.js` 会被当成 CommonJS 而顶层
 * await 不可用。
 *
 * 【为什么不依赖 gh】本机没有装 gh。git remote 走 SSH（推 tag 用 SSH key），
 * 而建 Release 要 PAT——两个凭据来源不同，这个脚本只负责后者。
 *
 * 用法：
 *   node scripts/release.mjs 3.0 --dry-run     只打印，不碰 git、不联网
 *   node scripts/release.mjs 3.0               真的发布
 *   node scripts/release.mjs --check-changelog 只校验 CHANGELOG 格式
 *   node scripts/release.mjs 1.0 --allow-existing-tag  补建历史版本的 Release
 */

import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const REPO = 'XiaoleC05/CongoRAG'
const API = `https://api.github.com/repos/${REPO}`
const CHANGELOG = 'CHANGELOG.md'
const CONTRACT = 'contracts/openapi.yaml'

// ── 小工具 ──────────────────────────────────────────────────────

function fail(message) {
  console.error(`\n✗ ${message}\n`)
  process.exit(1)
}

function git(...args) {
  return execFileSync('git', args, { encoding: 'utf8' }).trim()
}

function gitQuiet(...args) {
  try {
    return { ok: true, out: git(...args) }
  } catch (err) {
    return { ok: false, out: String(err.stderr ?? err.message) }
  }
}

// ── CHANGELOG ───────────────────────────────────────────────────

/**
 * 抽出某个版本的小节正文。
 *
 * 【终止条件必须是两个】只看 `^## ` 的话，最后一个版本的小节会把文件底部的
 * compare 链接定义一起吞进 Release 正文（它们长成 `[1.0]: https://...`，
 * 不是 `## ` 开头）。所以同时把 `^[...]:` 当终止符。
 */
function changelogSection(text, version) {
  const lines = text.split('\n')
  const start = lines.findIndex((l) => l.startsWith(`## [${version}]`))
  if (start === -1) return null

  const body = []
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i]
    if (line.startsWith('## ') || /^\[[^\]]+\]:/.test(line)) break
    body.push(line)
  }
  return body.join('\n').trim()
}

/** 校验 CHANGELOG 的结构：每个版本小节都有日期、降序、且至少一个 ### 子标题。 */
function checkChangelog(text) {
  const problems = []
  const headings = [...text.matchAll(/^## \[([^\]]+)\](.*)$/gm)]

  let lastDate = null
  for (const h of headings) {
    const [, version, rest] = h
    if (version === 'Unreleased') continue

    const dateMatch = rest.match(/- (\d{4}-\d{2}-\d{2})/)
    if (!dateMatch) problems.push(`## [${version}] 没有 YYYY-MM-DD 日期`)
    else if (lastDate !== null && dateMatch[1] > lastDate) {
      problems.push(`## [${version}] 的日期比它下面那个版本还新（版本应降序）`)
    } else lastDate = dateMatch[1]

    const section = changelogSection(text, version)
    if (!section || !/^### /m.test(section)) {
      problems.push(`## [${version}] 里没有任何 ### 子标题`)
    }
  }
  return problems
}

/** 契约版本（#53 选 A：它与产品版本是同一个数）。 */
function contractVersion(text) {
  const lines = text.split('\n')
  const infoAt = lines.findIndex((l) => l.startsWith('info:'))
  if (infoAt === -1) return null
  for (let i = infoAt + 1; i < lines.length; i++) {
    if (/^\S/.test(lines[i])) break // 走出 info: 块
    const m = lines[i].match(/^\s+version:\s*"?([^"\s]+)"?\s*$/)
    if (m) return m[1]
  }
  return null
}

// ── 凭据 ────────────────────────────────────────────────────────

/**
 * 按 GITHUB_TOKEN → GH_TOKEN → 仓库根的 .env 顺序找 token。
 *
 * 【任何路径下都不打印 token】连长度都不打——错误信息里只提变量名。
 */
function readToken() {
  for (const name of ['GITHUB_TOKEN', 'GH_TOKEN']) {
    const v = process.env[name]
    if (v && v.trim()) return { token: v.trim(), from: `环境变量 ${name}` }
  }

  if (existsSync('.env')) {
    for (const line of readFileSync('.env', 'utf8').split('\n')) {
      const trimmed = line.trim()
      if (!trimmed || trimmed.startsWith('#')) continue
      const eq = trimmed.indexOf('=')
      if (eq === -1) continue
      const key = trimmed.slice(0, eq).trim()
      if (key === 'GITHUB_TOKEN' || key === 'GH_TOKEN') {
        const value = trimmed.slice(eq + 1).trim()
        if (value) return { token: value, from: `.env 的 ${key}` }
      }
    }
  }
  return null
}

// ── GitHub API ──────────────────────────────────────────────────

async function api(token, method, path, body) {
  const resp = await fetch(`${API}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
      ...(body ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await resp.text()
  return { status: resp.status, ok: resp.ok, json: text ? JSON.parse(text) : null }
}

// ── 主流程 ──────────────────────────────────────────────────────

const argv = process.argv.slice(2)
const flags = new Set(argv.filter((a) => a.startsWith('--')))
const version = argv.find((a) => !a.startsWith('--'))
const dryRun = flags.has('--dry-run')
const allowExistingTag = flags.has('--allow-existing-tag')

if (flags.has('--check-changelog')) {
  if (!existsSync(CHANGELOG)) fail(`找不到 ${CHANGELOG}`)
  const problems = checkChangelog(readFileSync(CHANGELOG, 'utf8'))
  if (problems.length) fail(`CHANGELOG 有问题：\n  - ${problems.join('\n  - ')}`)
  console.log('✓ CHANGELOG 格式没问题')
  process.exit(0)
}

if (!version) {
  fail('用法：node scripts/release.mjs <版本> [--dry-run]\n  例如：node scripts/release.mjs 3.0')
}
if (!/^\d+\.\d+(\.\d+)?$/.test(version)) {
  fail(`版本号 "${version}" 看起来不对——写成 MAJOR.MINOR（热修才加第三位）。`)
}

const tag = `v${version}`

// 1. CHANGELOG 必须有这一节
if (!existsSync(CHANGELOG)) fail(`找不到 ${CHANGELOG}`)
const changelog = readFileSync(CHANGELOG, 'utf8')
const body = changelogSection(changelog, version)
if (!body) {
  fail(`${CHANGELOG} 里没有 "## [${version}]" 这一节。\n  先写 CHANGELOG 再发布——它是 tag 消息与 Release 正文的唯一来源。`)
}

// 2. 契约版本必须跟产品版本一致（ADR-002）
//
// 【--allow-existing-tag 时跳过】那个开关的用途是「补建历史版本的 Release」
// （v1.0 当时只推了 tag、没建 Release）。契约版本跟的是**当前**产品版本，
// 补建一个旧版本时它当然不等于那个旧号——拿它去挡补建是错的。
// 断言管的是"新发布一个版本"，不是"给旧 tag 补一个 Release 对象"。
const gotContract = contractVersion(readFileSync(CONTRACT, 'utf8'))
if (allowExistingTag) {
  if (gotContract !== version) {
    console.log(`· 补建历史版本：契约版本是 "${gotContract}"（当前产品版本），跳过一致性断言`)
  }
} else if (gotContract !== version) {
  fail(
    `${CONTRACT} 的 info.version 是 "${gotContract}"，而要发布的是 "${version}"。\n` +
      `  契约版本跟产品版本走（ADR-002），发布前必须先把那一行改成 "${version}"。`,
  )
}

// 3. 工作区、分支、远端
const status = git('status', '--porcelain')
if (status) {
  fail(`工作区不干净，先提交或 stash：\n${status}`)
}
const branch = git('rev-parse', '--abbrev-ref', 'HEAD')
if (branch !== 'main') fail(`当前在 "${branch}" 上，发布必须在 main 上。`)

// 【dry-run 不 fetch】`git fetch` 会联网，而 --dry-run 的承诺是"不碰 git、不联网"
// （脚本头、`make help`、docs/releasing.md 三处都这么写）。这一条检查挪到
// dry-run 分支之后——真发布时它必须在，dry-run 时跳过并如实说明。
if (!dryRun) {
  git('fetch', 'origin', 'main')
  if (git('rev-parse', 'HEAD') !== git('rev-parse', 'origin/main')) {
    fail('本地 main 与 origin/main 不一致，先 pull 或 push。')
  }
}

// 4. tag 不能已经存在（--allow-existing-tag 用于补建历史版本的 Release）
//
// 【dry-run 不查远端】`git ls-remote` 要走 SSH 到 origin，而 --dry-run 的承诺是
// "不碰 git、不联网"（脚本头、`make help`、docs/releasing.md 三处都这么写）。
// 和上面 `git fetch` 那条同样的处理：真发布时必须查，dry-run 时只查本地 tag，
// 并在输出里把"远端没查"这件事写出来——承诺被破坏的地方，正是它最该成立的地方。
// 代价是 dry-run 的"新建还是补建"只按本地判断，所以输出里必须说明这一点，
// 不能让一个没做过的检查看起来像做过了。
const localTag = gitQuiet('rev-parse', '-q', '--verify', `refs/tags/${tag}`)
const remoteTag = dryRun ? '' : git('ls-remote', '--tags', 'origin', `refs/tags/${tag}`)
const tagExists = localTag.ok || remoteTag !== ''

if (tagExists && !allowExistingTag) {
  fail(`tag ${tag} 已经存在。确实要补建它的 Release 就加 --allow-existing-tag。`)
}
if (!tagExists && allowExistingTag) {
  fail(`加了 --allow-existing-tag 但 tag ${tag} 并不存在——去掉这个参数重新跑。`)
}

// 5. 凭据
const cred = readToken()

// ── dry-run 到此为止 ──
if (dryRun) {
  console.log(`\n=== dry-run：${tag} ===\n`)
  console.log(`契约版本检查：${gotContract} ✓`)
  console.log('与 origin/main 是否一致：未检查（dry-run 不联网）')
  console.log(`tag 是否已存在：${tagExists ? '是（将走 PATCH 分支）' : '否（将新建）'}——只查了本地 tag`)
  console.log('远端是否已有这个 tag：未检查（dry-run 不联网）——上面 POST/PATCH 的选择只按本地判断')
  console.log(`凭据：${cred ? `来自 ${cred.from}` : '未找到（真发布会失败）'}`)
  console.log(`\n--- tag 消息 / Release 正文 ---\n${body}\n--- 正文结束 ---\n`)
  console.log('将要执行的命令：')
  if (!tagExists) {
    console.log(`  git tag -a ${tag} -F <临时文件>`)
    console.log(`  git push origin refs/tags/${tag}`)
  }
  console.log(`  ${tagExists ? 'PATCH' : 'POST'} ${API}/releases${tagExists ? `/tags/${tag}` : ''}`)
  process.exit(0)
}

if (!cred) {
  fail(
    '找不到 GitHub 凭据。需要一个 fine-grained PAT：\n' +
      '  · 权限：Contents: read and write（建 Release 用）\n' +
      '  · 作用域：只勾这个仓库\n' +
      '  放到环境变量 GITHUB_TOKEN 或 GH_TOKEN，或者写进仓库根的 .env。',
  )
}

// 6. 打 tag（消息 = CHANGELOG 小节）
//
// 【必须用 -F 传文件】`git tag -a -m "<多行正文>"` 在 Windows 上有 argv 长度
// 与换行转义的风险，而这个正文是几千字。
if (!tagExists) {
  const tmp = join(tmpdir(), `congorag-release-${tag}-${process.pid}.md`)
  writeFileSync(tmp, body, 'utf8')
  try {
    git('tag', '-a', tag, '-F', tmp)
  } finally {
    rmSync(tmp, { force: true })
  }
  console.log(`✓ 建了 tag ${tag}`)

  git('push', 'origin', `refs/tags/${tag}`)
  console.log(`✓ 推了 tag ${tag}`)
}

// 7. 建或更新 Release
//
// 【先 GET 再决定 POST 还是 PATCH】脚本先推 tag 再调 API，而 API 可能失败
// （网络、权限、限流）——那时留下的是"有 tag 没 Release"的状态，正是 v1.0
// 的现状。幂等让重跑能补齐，而不是要求人去手工收拾。
const compareUrl = `https://github.com/${REPO}/compare/${tag}...HEAD`
const releaseBody = `${body}\n\n**完整变更**：${compareUrl}\n`

const existing = await api(cred.token, 'GET', `/releases/tags/${tag}`)
if (existing.ok) {
  const patched = await api(cred.token, 'PATCH', `/releases/${existing.json.id}`, {
    body: releaseBody,
    name: `ConGoRAG ${tag}`,
  })
  if (!patched.ok) fail(`更新 Release 失败：HTTP ${patched.status}\n${JSON.stringify(patched.json, null, 2)}`)
  console.log(`✓ 更新了已有的 Release ${tag}\n  ${patched.json.html_url}`)
} else if (existing.status === 404) {
  const created = await api(cred.token, 'POST', '/releases', {
    tag_name: tag,
    name: `ConGoRAG ${tag}`,
    body: releaseBody,
    draft: false,
    prerelease: false,
  })
  if (!created.ok) fail(`建 Release 失败：HTTP ${created.status}\n${JSON.stringify(created.json, null, 2)}`)
  console.log(`✓ 建了 Release ${tag}\n  ${created.json.html_url}`)
} else {
  fail(`查询 Release 失败：HTTP ${existing.status}\n${JSON.stringify(existing.json, null, 2)}`)
}

console.log('\n发布后请核对四件事（清单在 docs/releasing.md）：')
console.log('  1. Release 正文 == CHANGELOG 小节 + compare 链接')
console.log(`  2. tag ${tag} 指向的 commit 就是 main 顶端`)
// 【不写死 job 数量】这里从前写的是「四个 job」，后来 CI 加了 spec 和 docker
// 两条就成了假话，而没人会回来改这一行。清单的唯一真相是 ci.yml。
console.log('  3. CI 全绿（job 清单见 .github/workflows/ci.yml）')
console.log(`  4. 契约 info.version == ${version}`)
