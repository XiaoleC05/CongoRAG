// delivery-scripts.test.mjs —— upgrade / rollback / release 的冒烟门禁（issue #104 / #105）。
//
// 【为什么是"跑到某一步的报错"而不是完整跑一遍】这三条路径的完整执行需要一台
// Docker、一个跑着的 congorag-api、导进来的镜像，还有一份真备份——CI 上没有，
// 而"平时不跑、跑的时候不能错"恰恰是它们的性质：脚本被原样拷进启动包，唯一的
// 执行机会是用户的机器。所以这里卡的是**语法、模块导入、参数名、以及出错时的
// 那句话**：参数改了名 / delivery-lib 少导出一个函数 / rollback 又用未捕获的
// 异常收场，都会在这里红，而且每条断言都对着用户实际会看到的那一行。
//
// 【为什么不用 issue 原文里那条命令】原文建议 CI 跑
// `node scripts/upgrade.mjs --dir deployments/startup --dry-run`，并注明"纯打印、
// 不碰容器"。实际不是：`--dry-run` 分支排在"读正在跑的容器 / 查镜像是否在本地 /
// 探测 postgres 是否在跑"之后，而这些都要真 Docker 和一套活着的环境才能过。
// 所以那条命令在 CI 上会以"读不到容器"失败。这里改成把前置检查用**不可能存在的
// 容器名**逼到同一个分支——覆盖的是原文真正想覆盖的东西（参数与语法），
// 而且不依赖 Docker。
//
// 【dry-run 的"不写东西"也在这里钉住】每次调用之后都核对夹具目录没多出任何文件。

import { after, test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))

// 【用完就删】每个用例都会铺一个假启动包 / 假仓库，跑一次留下十几个临时目录
// 在别人的 Temp 里不合适。删失败也不影响结论，所以不关心它的报错。
const created = []
after(() => {
  for (const dir of created) rmSync(dir, { recursive: true, force: true })
})

function scratchDir(prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix))
  created.push(dir)
  return dir
}

const UPGRADE = join(HERE, 'upgrade.mjs')
const ROLLBACK = join(HERE, 'rollback.mjs')
const RELEASE = join(HERE, 'release.mjs')

// ── 小工具 ──────────────────────────────────────────────────────

/** 跑一个脚本，把失败也当成结果返回（这几个脚本的正常结局就包括非零退出）。 */
function runScript(file, args, { cwd = HERE, env = {} } = {}) {
  try {
    return {
      status: 0,
      stdout: execFileSync(process.execPath, [file, ...args], {
        cwd,
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'pipe'],
        env: { ...process.env, ...env },
      }),
      stderr: '',
    }
  } catch (err) {
    return {
      status: err.status ?? -1,
      stdout: String(err.stdout ?? ''),
      stderr: String(err.stderr ?? ''),
    }
  }
}

function git(cwd, args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
}

const hasGit = (() => {
  try {
    execFileSync('git', ['--version'], { stdio: 'ignore' })
    return true
  } catch {
    return false
  }
})()

/** 一个最小可用的"启动包目录"：compose 文件 + .env 就够了（两个脚本的前置检查都只看这两个）。 */
function packageDir() {
  const dir = scratchDir('congorag-scripts-')
  writeFileSync(join(dir, 'docker-compose.yml'), 'services: {}\n')
  writeFileSync(join(dir, '.env'), 'CONGORAG_VERSION=9.9\n')
  return dir
}

function snapshot(dir) {
  return readdirSync(dir).sort()
}

/** 断言目录里**恰好**是这些条目（两边都排序，"没多出东西"和"没少东西"一起卡）。 */
function assertFiles(dir, names, message) {
  assert.deepEqual(snapshot(dir), [...names].sort(), message)
}

/** 造一份备份目录。raw 给定时直接写进 manifest.json（用来造坏清单）。 */
function backup(root, name, raw) {
  const dir = join(root, 'backups', name)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'manifest.json'), raw)
  return dir
}

// ── upgrade.mjs ─────────────────────────────────────────────────

test('upgrade.mjs：文档里的开关都还在（--help 退出码 0）', () => {
  const res = runScript(UPGRADE, ['--help'])
  assert.equal(res.status, 0)
  // 这些名字同时出现在 docs/upgrading.md、Makefile 和脚本头里。改掉任何一个
  // 而不改文档，就是在制造"文档说的命令跑不起来"。
  for (const flag of ['--dir', '--to', '--backup-dir', '--pg', '--api', '--archive-image', '--dry-run']) {
    assert.ok(res.stdout.includes(flag), `--help 里应该有 ${flag}`)
  }
})

test('upgrade.mjs：参数面能走通到前置检查，且报错点名了容器', () => {
  const dir = packageDir()
  const before = snapshot(dir)

  // 【为什么用"不可能存在的容器名"】imageOf 读不到镜像时脚本会以一句明确的
  // 话停下，而这条路**不依赖 Docker 在不在**（docker 命令失败与容器不存在
  // 都归成 null）。于是它成了一个不需要 Docker 的门禁：脚本能加载、
  // delivery-lib 的导出都在、--dir/--api 还认、.env 还被读到。
  const res = runScript(UPGRADE, [
    '--dir', dir,
    '--api', 'congorag-no-such-container-for-tests',
    '--dry-run',
  ])

  assert.notEqual(res.status, 0, '读不到容器时必须失败，不能继续往下做')
  assert.match(res.stderr, /读不到容器 congorag-no-such-container-for-tests/)
  // 参数改名的话会退回默认的 congorag-api，上面那条断言就会红——这正是
  // issue 里"把参数改名挡在发布前"要的效果。
  assert.doesNotMatch(res.stderr, /at file:\/\/|SyntaxError|TypeError/, '不能是崩出来的栈')

  assert.deepEqual(snapshot(dir), before, 'dry-run 不能写出任何东西')
  assert.equal(existsSync(join(dir, 'backups')), false, '更不该建 backups/')
})

// ── rollback.mjs ────────────────────────────────────────────────

test('rollback.mjs：文档里的开关都还在（--help 退出码 0）', () => {
  const res = runScript(ROLLBACK, ['--help'])
  assert.equal(res.status, 0)
  for (const flag of ['--dir', '--backup', '--pg', '--api', '--archive-image', '--dry-run', '--yes']) {
    assert.ok(res.stdout.includes(flag), `--help 里应该有 ${flag}`)
  }
  // 它会丢数据这件事必须写在 --help 里：这是唯一一个"少打一个参数就毁库"的脚本。
  assert.match(res.stdout, /它会丢数据/)
})

test('rollback.mjs：一份备份都没有时，说的是"找不到备份"而不是别的', () => {
  const dir = packageDir()
  const res = runScript(ROLLBACK, ['--dir', dir, '--dry-run'])
  assert.notEqual(res.status, 0)
  assert.match(res.stderr, /找不到备份/)
  assertFiles(dir, ['.env', 'docker-compose.yml'], '什么都没动')
})

test('rollback.mjs：清单损坏时不抛解析异常，而是点名文件 + 给出两条出路 (#104)', () => {
  const dir = packageDir()
  // 写了一半的 manifest：原来这一份会让脚本在 sort 的比较器里
  // JSON.parse 抛出未捕获的 SyntaxError，栈里只有 node 的内部路径。
  const broken = backup(dir, '2026-09-20T10-00-00', '{"createdAt": "2026-09-2')

  const res = runScript(ROLLBACK, ['--dir', dir, '--dry-run'])

  assert.notEqual(res.status, 0)
  assert.match(res.stderr, /读不出清单/, '要说明是清单读不出来')
  assert.ok(res.stderr.includes(broken), '要点名是哪一份备份')
  assert.match(res.stderr, /Unterminated string/, '要带上原始错误，而不是只说"失败了"')
  assert.match(res.stderr, /--backup/, '要给出"换一份"这条出路')
  // 最终判据：不能是未捕获的异常。栈里会出现 at JSON.parse / file:///... 这类
  // 只有开发者看得懂的东西，而那正是 issue 要修掉的症状。
  assert.doesNotMatch(res.stderr, /SyntaxError/, '不能再以未捕获的解析异常收场')
  assert.doesNotMatch(res.stderr, /at JSON\.parse/)
  assert.doesNotMatch(res.stderr, /at file:\/\//)
  assertFiles(dir, ['.env', 'docker-compose.yml', 'backups'], '没动任何数据')
})

test('rollback.mjs：--backup 指到损坏的清单，同样是可操作的报错 (#104)', () => {
  // 上一条的报错会把人指到 --backup，所以那条出路自己也不能是栈：
  // 显式指定的路径不再走"挑最新"，但清单校验那一关照样要能读懂。
  const dir = packageDir()
  const broken = backup(dir, 'broken', '{ 不是 JSON')

  const res = runScript(ROLLBACK, ['--dir', dir, '--backup', broken, '--dry-run'])

  assert.notEqual(res.status, 0)
  assert.ok(res.stderr.includes(join(broken, 'manifest.json')), '要点名那份清单')
  assert.match(res.stderr, /已经损坏/)
  assert.match(res.stderr, /还没有动任何数据/, '要说清楚现在还没造成任何破坏')
  assert.doesNotMatch(res.stderr, /SyntaxError|at JSON\.parse|at file:\/\//)
})

// ── release.mjs ─────────────────────────────────────────────────

/**
 * 一个自带坏 origin 的假仓库。
 *
 * 【origin 指向一个不存在的路径】这样远端**一定**不可达：dry-run 只要真去
 * ls-remote，就会以 git 的报错退出——那正是 issue #105 的症状。它同时保证了
 * 这个用例不会联网（本地路径立刻失败，不产生任何网络流量）。
 */
function releaseRepo({ tag } = {}) {
  const dir = scratchDir('congorag-release-')
  git(dir, ['init'])
  git(dir, ['symbolic-ref', 'HEAD', 'refs/heads/main'])
  git(dir, ['config', 'user.email', 'ci@example.invalid'])
  git(dir, ['config', 'user.name', 'CI'])
  git(dir, ['config', 'commit.gpgsign', 'false'])

  writeFileSync(
    join(dir, 'CHANGELOG.md'),
    '# 更新日志\n\n## [9.9] - 2026-01-01\n\n### 新增\n\n- 假的发布内容。\n\n[9.9]: https://example.invalid/compare\n',
  )
  mkdirSync(join(dir, 'contracts'), { recursive: true })
  writeFileSync(
    join(dir, 'contracts', 'openapi.yaml'),
    'openapi: 3.0.0\ninfo:\n  title: stub\n  version: "9.9"\npaths: {}\n',
  )

  git(dir, ['add', '.'])
  git(dir, ['commit', '-m', 'fixture'])
  git(dir, ['remote', 'add', 'origin', join(dir, '这个远端不存在')])
  if (tag) git(dir, ['tag', '-a', tag, '-m', 'stub'])
  return dir
}

// 【两个 token 都清空】不然 readToken 会读到这个 shell 里的 GITHUB_TOKEN，
// 输出就随环境变了。dry-run 本来也不该用凭据。
const NO_TOKEN = { GITHUB_TOKEN: '', GH_TOKEN: '' }

test('release.mjs：远端不可达时 --dry-run 仍然成功，且说明远端的 tag 没查 (#105)', { skip: !hasGit && '没装 git' }, () => {
  const dir = releaseRepo()

  // 【反向对照】先证明这个 origin 真的不可达。少了这一步，就算 dry-run 去查了
  // 远端、而远端恰好能连上，这个用例也会"通过"——那就什么都没证明。
  let remoteOk = true
  try {
    git(dir, ['ls-remote', '--tags', 'origin', 'refs/tags/v9.9'])
  } catch {
    remoteOk = false
  }
  assert.equal(remoteOk, false, '这个用例的前提是 origin 不可达')

  const res = runScript(RELEASE, ['9.9', '--dry-run'], { cwd: dir, env: NO_TOKEN })

  assert.equal(res.status, 0, `dry-run 不该被网络/远端挡下来：\n${res.stderr}`)
  assert.match(res.stdout, /未检查（dry-run 不联网）/, '要如实说明远端没有检查')
  assert.match(res.stdout, /只查了本地 tag/, '要说清楚 tag 的存在性只按本地判断')
  assert.match(res.stdout, /--- tag 消息 \/ Release 正文 ---/, '该打印的内容还是要打印')
})

test('release.mjs：本地已有这个 tag 时，dry-run 仍然查得到（PATCH 分支）', { skip: !hasGit && '没装 git' }, () => {
  // 上一条只证明了"不再查远端"，这一条证明"本地那一份还在查"——两条合起来
  // 才是那次改动的完整效果：拆开本地/远端，而不是把检查整个删掉。
  const dir = releaseRepo({ tag: 'v9.9' })
  const res = runScript(RELEASE, ['9.9', '--dry-run', '--allow-existing-tag'], { cwd: dir, env: NO_TOKEN })

  assert.equal(res.status, 0, res.stderr)
  assert.match(res.stdout, /是（将走 PATCH 分支）/)
  assert.match(res.stdout, /PATCH https:\/\/api\.github\.com\/repos\/XiaoleC05\/CongoRAG\/releases\/tags\/v9\.9/)
})
