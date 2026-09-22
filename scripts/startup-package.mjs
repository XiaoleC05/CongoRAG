#!/usr/bin/env node
/**
 * 启动包打包脚本（issue #73）。
 *
 * 产出一个 zip，解压 → 导入镜像 → 起 → 打开引导页，中间不需要 Go / Node
 * 工具链，也不需要本机编译。
 *
 * 包里的东西（每一件都有理由，不是随手塞的）：
 *
 *   docker-compose.yml   postgres + migrate + api + worker 四个服务
 *   .env.example         用户唯一需要改的文件，必填项与可选项都写着
 *   README.md            60 秒快速开始
 *   images/…tar.gz       docker save 出来的三个镜像（api / worker / migrate）
 *   upgrade.mjs          升级脚本（与仓库里 scripts/ 下的是同一份）
 *   rollback.mjs         回滚脚本
 *   delivery-lib.mjs     上面两个共用的工具
 *   docs/upgrading.md    用户视角的升级/回滚步骤
 *
 * 【为什么升级脚本要放进包里，而不是只留在仓库】升级是"一直用"的事，
 * 而用户手里只有这个包。让他为了升级再去 clone 仓库，等于把"升级"变成了
 * "重新学一遍这个项目"。代价是包里多了一个 node 依赖：升级脚本是 .mjs，
 * 需要宿主机有 node ≥ 22。这一点写在 README 的"升级"一节里，不含糊。
 *
 * 【和 release.yml 的分工】镜像由 CI 的 matrix 按架构构建好、上传成
 * artifact；这个脚本在 CI 里只做"装配"（--images-archive 指向那个
 * artifact）。本地跑的时候（不带那个参数）它会自己 docker build 一遍。
 * 两条路都走同一个装配逻辑，所以本地验过的包和 CI 出来的包结构一致。
 *
 * 用法：
 *   node scripts/startup-package.mjs --version 3.0 --arch amd64
 *   node scripts/startup-package.mjs --version 3.0 --images-archive dist/images.tar.gz
 *   node scripts/startup-package.mjs --version 3.0 --dry-run
 */

import { spawn } from 'node:child_process'
import {
  copyFileSync,
  createWriteStream,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { join, resolve } from 'node:path'
import { createGzip } from 'node:zlib'
import { pipeline } from 'node:stream/promises'

import { ensureDir, fail, info, ok, run, sha256File, step, tryRun, writeZip } from './delivery-lib.mjs'

const REPO = resolve(import.meta.dirname, '..')

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
      '打包 ConGoRAG 启动包（zip：compose + .env.example + 镜像 tarball + 升级脚本）。',
      '',
      '  --version 版本    镜像 tag 与包名里用的版本（默认取契约的 info.version）',
      '  --arch 架构       amd64 | arm64（默认按本机 Docker 的架构）',
      '  --out 目录        产物写到哪里（默认 dist/）',
      '  --images-archive  已有的 docker save tarball（.tar.gz）。给了它就不再构建。',
      '  --dry-run         只打印将要做什么',
      '',
      '产物：<out>/congorag-startup-<版本>-linux-<架构>.zip',
    ].join('\n'),
  )
  process.exit(0)
}

const dryRun = hasFlag('--dry-run')
const version = argValue('--version', contractVersion())
const arch = argValue('--arch', hostArch())
const outDir = resolve(argValue('--out', 'dist'))
const imagesArchive = argValue('--images-archive', null)

if (!/^\d+\.\d+(\.\d+)?$/.test(version)) {
  fail(`版本号 "${version}" 看起来不对——写成 MAJOR.MINOR（热修才加第三位）。`)
}
if (!['amd64', 'arm64'].includes(arch)) {
  fail(`--arch 只支持 amd64 / arm64，拿到的是 "${arch}"。`)
}

const packageName = `congorag-startup-${version}-linux-${arch}`
const stageDir = join(outDir, packageName)
const zipPath = join(outDir, `${packageName}.zip`)
const imageTarballName = `congorag-images-linux-${arch}.tar.gz`

info(`版本    ${version}`)
info(`架构    ${arch}`)
info(`产物    ${zipPath}`)

if (dryRun) {
  console.log('\n=== dry-run：将要执行 ===\n')
  if (imagesArchive) {
    console.log(`  1. 用现成的镜像包 ${resolve(imagesArchive)}`)
  } else {
    for (const name of ['api', 'worker', 'migrate']) {
      console.log(`  1. docker build --platform linux/${arch} --target ${name} -t congorag-${name}:${version} -f Dockerfile .`)
    }
    console.log(`  1b. docker save 三个镜像 | gzip > ${join(stageDir, 'images', imageTarballName)}`)
  }
  console.log(`  2. 装配 ${stageDir}/（compose、.env.example、README、升级脚本、docs/upgrading.md、镜像）`)
  console.log(`  3. 打 zip → ${zipPath}`)
  console.log('\n（dry-run 到此为止，什么都没做）')
  process.exit(0)
}

// ── 1. 镜像 tarball ─────────────────────────────────────────────

step('准备镜像 tarball')

ensureDir(join(stageDir, 'images'))
const imageTarballPath = join(stageDir, 'images', imageTarballName)

if (imagesArchive) {
  const src = resolve(imagesArchive)
  if (!existsSync(src)) fail(`--images-archive 指向的文件不存在：${src}`)
  copyFileSync(src, imageTarballPath)
  ok(`用了现成的 ${src}`)
} else {
  const tags = []
  for (const name of ['api', 'worker', 'migrate']) {
    const tag = `congorag-${name}:${version}`
    tags.push(tag)
    info(`构建 ${tag}（linux/${arch}）…`)
    try {
      // --platform 在非本机架构时会走 QEMU，需要 binfmt 已注册
      //（本地 Docker Desktop 自带；CI 上要 docker/setup-qemu-action）。
      run('docker', [
        'build',
        '--platform', `linux/${arch}`,
        '--target', name,
        '-t', tag,
        '-f', join(REPO, 'Dockerfile'),
        REPO,
      ])
    } catch (err) {
      fail(
        `构建 ${tag} 失败：\n  ${String(err.stderr ?? err.message).trim()}\n` +
          (arch !== hostArch()
            ? `\n  在 ${hostArch()} 上构建 linux/${arch} 需要 QEMU（binfmt）支持。本地没有的话用 --images-archive 喂一个 CI 产出的包。`
            : ''),
      )
    }
    ok(`构建完成 ${tag}`)
  }

  // 【save 直接接 gzip 流，不落一个中间 tar】未压缩的 docker save 有几个
  // 百 MB，先写盘再压会白占一份磁盘、还让失败时的清理多一种情况。
  info('docker save | gzip …')
  const gz = createGzip({ level: 6 })
  const out = createWriteStream(imageTarballPath)
  const save = spawn('docker', ['save', ...tags], { stdio: ['ignore', 'pipe', 'pipe'] })
  let saveErr = ''
  save.stderr.on('data', (d) => {
    saveErr += d.toString()
  })
  // 【pipeline 而不是 .pipe()】pipeline 会把三条流串起来并在任何一条出错时
  // 一起收掉——.pipe() 不会，一旦 gz 出错，docker save 会继续往一个死掉的
  // 管道里写，进程卡在那儿不退出。
  //
  // 【为什么要单独等 close，不能读 save.exitCode】pipeline 在**最后一条流**
  // （文件）写完时就 resolve，那一刻子进程可能还没被回收，`save.exitCode`
  // 还是 null——拿它去判 `!== 0` 会把一次成功当成失败（实测踩到：报
  // "docker save 退出码 null"，而镜像包其实已经写好了）。等到 'close'
  // 事件才有确定的退出码。
  const saveExited = new Promise((resolve, reject) => {
    save.on('error', reject)
    save.on('close', (code) => resolve(code))
  })
  try {
    await pipeline(save.stdout, gz, out)
  } catch (err) {
    fail(`docker save 失败：${saveErr.trim() || err.message}`)
  }
  const code = await saveExited
  if (code !== 0) fail(`docker save 退出码 ${code}：${saveErr.trim()}`)
  ok(`镜像包 ${(statSync(imageTarballPath).size / 1024 / 1024).toFixed(1)} MiB`)
}
info(`sha256 ${sha256File(imageTarballPath)}`)

// ── 2. 装配目录 ─────────────────────────────────────────────────

step('装配启动包目录')

const startupSrc = join(REPO, 'deployments', 'startup')
const files = [
  [join(startupSrc, 'docker-compose.yml'), 'docker-compose.yml'],
  [join(startupSrc, 'README.md'), 'README.md'],
  [join(REPO, 'scripts', 'upgrade.mjs'), 'upgrade.mjs'],
  [join(REPO, 'scripts', 'rollback.mjs'), 'rollback.mjs'],
  [join(REPO, 'scripts', 'delivery-lib.mjs'), 'delivery-lib.mjs'],
  [join(REPO, 'docs', 'upgrading.md'), 'docs/upgrading.md'],
]
for (const [src, dest] of files) {
  if (!existsSync(src)) fail(`装配要用到的文件不存在：${src}`)
  const target = join(stageDir, dest)
  mkdirSync(resolve(target, '..'), { recursive: true })
  copyFileSync(src, target)
}

// .env.example 要单独处理：把 CONGORAG_VERSION 写成**这个包的版本**。
// 【为什么值得破一次"原样拷贝"的规矩】用户拿到包之后第一件事是 cp .env.example .env，
// 而 .env.example 里那个版本号是仓库里写死的 3.0——下一个版本发布时它会漂移，
// 表现是包里的镜像 tag 和 .env 里的对不上（"image congorag-api:3.1 not found"）。
// 在装配时写进去，这个坑就不存在了。
const envExamplePath = join(startupSrc, '.env.example')
if (!existsSync(envExamplePath)) {
  fail(`装配要用到的文件不存在：${envExamplePath}`)
}
const envExampleSrc = readFileSync(envExamplePath, 'utf8')
const envExample = envExampleSrc.replace(/^CONGORAG_VERSION=.*$/m, `CONGORAG_VERSION=${version}`)
if (envExample === envExampleSrc) {
  fail('没能在 .env.example 里找到 CONGORAG_VERSION= 那一行——它在装配时会被写成包的版本，找不到就不能继续。')
}
writeFileSync(join(stageDir, '.env.example'), envExample, 'utf8')
ok(`装配了 ${files.length + 2} 个文件（含 .env.example，版本已写成 ${version}）`)

// ── 3. 打 zip ───────────────────────────────────────────────────

step('打 zip')

const entries = [
  { name: 'docker-compose.yml', path: join(stageDir, 'docker-compose.yml') },
  { name: '.env.example', path: join(stageDir, '.env.example') },
  { name: 'README.md', path: join(stageDir, 'README.md') },
  { name: 'upgrade.mjs', path: join(stageDir, 'upgrade.mjs') },
  { name: 'rollback.mjs', path: join(stageDir, 'rollback.mjs') },
  { name: 'delivery-lib.mjs', path: join(stageDir, 'delivery-lib.mjs') },
  { name: 'docs/upgrading.md', path: join(stageDir, 'docs', 'upgrading.md') },
  { name: `images/${imageTarballName}`, path: imageTarballPath },
]
const { bytes, count } = writeZip(zipPath, entries)
ok(`${count} 个条目，${(bytes / 1024 / 1024).toFixed(1)} MiB`)

// 【打完就删暂存目录】它和 zip 内容一样大（那 99% 是镜像包），留着就是
// 白占一份磁盘。zip 才是产物。
rmSync(stageDir, { recursive: true, force: true })

// ── 4. 校验产物 ─────────────────────────────────────────────────

step('校验产物')

// 【为什么要读回来验一遍】这个 zip 是自己拼字节拼出来的（见 delivery-lib.mjs
// 的 writeZip）：头部字段写错的话，本地"生成成功"什么也不会报，失败发生在
// 用户解压的那一刻。所以用解压工具**读一遍**，读得出来才算数。
// Windows 自带 tar（bsdtar）能读 zip；Linux 上优先 unzip。
const listed = tryRun('unzip', ['-l', zipPath]) ?? tryRun('tar', ['-tf', zipPath])
if (!listed) {
  fail(`zip 写出来了但读不回来：${zipPath}\n  用任何解压工具试一下，若确实坏了就是 writeZip 的头部字段有问题。`)
}
const listedNames = listed.split('\n').map((l) => l.trim())
for (const e of entries) {
  if (!listedNames.some((l) => l.endsWith(e.name) || l === e.name)) {
    fail(`zip 里没找到条目 ${e.name}。`)
  }
}
ok(`zip 可解压，${entries.length} 个条目都在`)

console.log('\n════════ 启动包完成 ════════')
console.log(`  ${zipPath}`)
console.log(`  sha256 ${sha256File(zipPath)}`)
console.log('')
console.log('  清洁环境实测：解压到一个空目录 → docker load -i images/… → cp .env.example .env')
console.log('                → docker compose up -d → 打开 http://127.0.0.1:3210')

// ── 小工具 ──────────────────────────────────────────────────────

/** 契约里的 info.version——和 scripts/release.mjs 的 contractVersion 同一套判据。 */
function contractVersion() {
  const lines = readFileSync(join(REPO, 'contracts', 'openapi.yaml'), 'utf8').split('\n')
  const infoAt = lines.findIndex((l) => l.startsWith('info:'))
  if (infoAt === -1) return null
  for (let i = infoAt + 1; i < lines.length; i++) {
    if (/^\S/.test(lines[i])) break // 走出 info: 块
    const m = lines[i].match(/^\s+version:\s*"?([^"\s]+)"?\s*$/)
    if (m) return m[1]
  }
  return null
}

/** 本机 Docker 的架构，映射成 Go 的 GOARCH 写法。 */
function hostArch() {
  const raw = tryRun('docker', ['info', '--format', '{{.Architecture}}']) ?? ''
  if (raw.includes('aarch64') || raw.includes('arm64')) return 'arm64'
  return 'amd64'
}
