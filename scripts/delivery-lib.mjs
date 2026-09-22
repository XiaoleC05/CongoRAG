#!/usr/bin/env node
/**
 * 交付形态（启动包那套环境）的共用工具（issue #73 / #74）。
 *
 * 【为什么要有这个文件】upgrade.mjs 与 rollback.mjs 各要一半同样的东西：
 * 调 docker compose、走容器执行 psql/pg_dump、读写真有 `.env` 文件、
 * 解析卷名与镜像 tag。抄两遍的下场不是"多写几十行"，是**两边对同一件事
 * 给出不同答案**——比如一个按容器名找 postgres、另一个按 compose 服务名找，
 * 在"用户手工起过同名容器"这个场景下就会一个成功一个失败。
 *
 * 【这里的东西全部只读或幂等，没有一步会改用户的数据】写动作留在两个
 * 脚本里，这样"回滚会清库"这种话只有一个地方需要核对。
 */

import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { crc32 } from 'node:zlib'

// 【这个上限不是随便写的】pg_dump 的自定义格式输出会整个进内存
// （见 dumpDatabase 的注释），一个几百 MB 的库会直接撞上默认的 1MB 上限，
// 报一句和真实原因无关的 "maxBuffer length exceeded"。1 GiB 远大于本项目
// 面向的"个人知识库"规模（32 MiB 单文件上限、几百份文档），又还留着上限。
export const MAX_BUFFER = 1 << 30

export function fail(message) {
  console.error(`\n✗ ${message}\n`)
  process.exit(1)
}

export function step(message) {
  console.log(`\n▸ ${message}`)
}

export function info(message) {
  console.log(`  ${message}`)
}

export function ok(message) {
  console.log(`  ✓ ${message}`)
}

/** 跑一个外部命令，返回 stdout（已编码为字符串、去尾换行）。失败抛异常。 */
export function run(cmd, args, opts = {}) {
  return execFileSync(cmd, args, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    maxBuffer: MAX_BUFFER,
    ...opts,
  }).trim()
}

/** 同 run，但失败返回 null（用于"探测"类调用：查得到/查不到都是正常结果）。 */
export function tryRun(cmd, args, opts = {}) {
  try {
    return run(cmd, args, opts)
  } catch {
    return null
  }
}

/** 跑一个外部命令，返回原始 Buffer（二进制输出，比如 pg_dump）。 */
export function runBinary(cmd, args) {
  return execFileSync(cmd, args, { stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: MAX_BUFFER })
}

/** 跑一个外部命令，把 `input`（Buffer）喂给它的 stdin，返回 stdout 字符串。 */
export function runWithInput(cmd, args, input) {
  return execFileSync(cmd, args, {
    input,
    encoding: 'utf8',
    stdio: ['pipe', 'pipe', 'pipe'],
    maxBuffer: MAX_BUFFER,
  })
}

// ── docker compose ──────────────────────────────────────────────

/**
 * 构造一个"在这个目录里跑 compose"的调用器。
 *
 * 【为什么每条命令都显式带 -f】不写 -f 的话 compose 从**当前工作目录**
 * 往上找 docker-compose.yml，而升级脚本的工作目录可能是仓库根（`make upgrade`
 * 就是），也可能是解压出来的启动包目录。写死 -f <dir>/docker-compose.yml
 * 让两种场合都成立。
 *
 * 【为什么还带 --project-directory】compose 的 `.env` 是按 -f 所在目录找的，
 * 但相对路径的挂载点是按 project directory 解析的。两者都指到同一个目录，
 * 行为才和"用户 cd 进去再 docker compose up"完全一致——否则会出现
 * "脚本说起来了、用户自己敲却起不来"这种最难查的差异。
 */
export function composer(dir) {
  const composeFile = join(dir, 'docker-compose.yml')
  const projectDir = dir
  const base = ['compose', '-f', composeFile, '--project-directory', projectDir]
  return {
    composeFile,
    projectDir,
    /** 跑一条 compose 子命令并返回 stdout。 */
    run: (args, opts = {}) => run('docker', [...base, ...args], opts),
    tryRun: (args, opts = {}) => tryRun('docker', [...base, ...args], opts),
    /** 跑一条 compose 子命令，把 stdout 直接接到逐行回调（用于 logs 之类）。 */
    spawnArgs: (args) => ['docker', [...base, ...args]],
  }
}

/** 走容器执行 psql，返回 `-tA` 的原始输出。 */
export function pgSql(container, query) {
  return run('docker', [
    'exec', container,
    'psql', '-U', 'postgres', '-d', 'congorag',
    '-tA', '-F', '|', '-c', query,
  ])
}

/** 走容器执行 psql，但对另一个库（回滚时 drop/create 要连 postgres 库）。 */
export function pgSqlOn(container, database, query) {
  return run('docker', [
    'exec', container,
    'psql', '-U', 'postgres', '-d', database,
    '-tA', '-F', '|', '-c', query,
  ])
}

/**
 * 把 `docker inspect` 模板的输出解析成卷名。
 *
 * 【为什么单独一个函数】"空"在这条路径上有三种来源：容器不在（`tryRun` 给的是
 * null）、destination 没有匹配的挂载（模板输出一串空行）、以及真的取到了名字。
 * 三者必须归成同一个结论（null / 名字），而这段判断是本模块里**唯一能脱离
 * docker 被测的部分**——volumeNameOf 剩下的工作只是把命令输出喂进来。
 */
export function parseVolumeName(raw) {
  if (raw === null || raw === undefined) return null
  const name = String(raw).trim()
  return name ? name : null
}

/**
 * 从一个容器上挂到 <destination> 的卷里取出卷名。读不到返回 null。
 *
 * 【为什么不按 `docker volume ls` 猜名字】卷名默认是
 * `<compose 项目名>_<卷名>`，而项目名会随 `name:` 字段、目录名、
 * `-p` 参数变。用 `docker inspect` 读**实际挂上去的那个**，是唯一不受
 * 这些规则影响的做法。
 */
export function volumeNameOf(container, destination = '/data') {
  return parseVolumeName(
    tryRun('docker', [
      'inspect', container,
      '-f', `{{range .Mounts}}{{if eq .Destination "${destination}"}}{{.Name}}{{end}}{{end}}`,
    ]),
  )
}

/** 容器当前跑的是哪个镜像 tag（如 congorag-api:3.0）。容器不在则 null。 */
export function imageOf(container) {
  const ref = tryRun('docker', ['inspect', container, '-f', '{{.Config.Image}}'])
  return ref && ref.trim() ? ref.trim() : null
}

/** 已经 `docker load` 进来的镜像里有这个 tag 吗。 */
export function imageExists(ref) {
  return tryRun('docker', ['image', 'inspect', ref, '-f', '{{.Id}}']) !== null
}

// ── 文件与环境变量 ──────────────────────────────────────────────

export function sha256File(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex')
}

/**
 * 读真 `.env` 文件的键值（不引 shell、不引 dotenv）。
 *
 * 【只认 KEY=VALUE，不处理引号与 ${} 展开】这是刻意的：升级脚本只需要
 * 读 CONGORAG_VERSION 和 CONGORAG_DB_URL 两个值，而这两个在
 * .env.example 里的写法就是裸值。真要做完整的 dotenv 解析，就得把
 * "引号怎么剥""变量怎么展开"这些规则也实现一遍，而它们和 compose 自己的
 * 实现稍有出入就会让脚本和 compose 对同一个文件得出不同的结论——
 * 那比读不到值更糟。
 */
export function readEnvFile(path) {
  const out = {}
  if (!existsSync(path)) return out
  for (const line of readFileSync(path, 'utf8').split('\n')) {
    const trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('#')) continue
    const eq = trimmed.indexOf('=')
    if (eq === -1) continue
    out[trimmed.slice(0, eq).trim()] = trimmed.slice(eq + 1).trim()
  }
  return out
}

/**
 * 就地改 `.env` 里某一个键的值；键不存在就追加到末尾。
 *
 * 【为什么是"就地改"而不是重写整个文件】用户会在 .env 里写注释、留自己
 * 记的笔记。重写会把它们全抹掉，而这是一次升级里最不该发生的事——
 * 用户下次打开那个文件会认不出来。
 */
export function setEnvVar(path, key, value) {
  const text = readFileSync(path, 'utf8')
  const lines = text.split('\n')
  const re = new RegExp(`^\\s*${key}\\s*=`)
  let found = false
  for (let i = 0; i < lines.length; i++) {
    if (re.test(lines[i])) {
      lines[i] = `${key}=${value}`
      found = true
      break
    }
  }
  if (!found) lines.push(`${key}=${value}`)
  writeFileSync(path, lines.join('\n'), 'utf8')
}

export function ensureDir(path) {
  mkdirSync(path, { recursive: true })
  return path
}

/** 备份目录的默认根。相对 --dir，这样"在启动包目录里跑"和"在仓库里跑"都自然。 */
export function defaultBackupRoot(dir) {
  return resolve(dir, 'backups')
}

/**
 * 在 backupRoot 下挑最新的一份备份目录。
 *
 * 【按清单里的 createdAt 排，不按目录名】目录名是 `--backup-dir` 给什么就是
 * 什么，用户手工挪一下、解压一次就不可信了；清单里的时间戳是升级脚本自己
 * 写下来的，描述的是"这份备份是什么时候做的"，那才是"新"的定义。
 *
 * 【为什么把读不出清单的收集起来、而不是直接跳过】回滚是最后一次机会。把一份
 * "看起来是备份、但清单读不出来"的目录悄悄跳过、退回到更旧的一份，用户以为在
 * 还原 A、实际还原了 B——那比停下来把话说清楚糟得多。这里只负责收集，怎么处理
 * 由 rollback.mjs 定（它才是面向用户的那一端）。
 *
 * 【没有 manifest.json 的不算"损坏"】那种目录根本不是备份（可能是用户自己放的
 * 东西），损坏指的是"看起来是备份、但读不出来"。
 *
 * @returns {{dir: string|null, corrupt: {path: string, reason: string}[]}}
 */
export function pickNewestBackup(root) {
  if (!existsSync(root)) return { dir: null, corrupt: [] }

  const corrupt = []
  const dated = []
  for (const name of readdirSync(root)) {
    const entry = join(root, name)
    const manifestPath = join(entry, 'manifest.json')
    if (!existsSync(manifestPath)) continue
    try {
      const createdAt = JSON.parse(readFileSync(manifestPath, 'utf8')).createdAt
      // 【缺 createdAt 也要当成损坏】排序靠它，undefined 会让比较器抛
      // "Cannot read properties of undefined"——那正是这个函数要避免的那种
      // 莫名其妙的崩法，换成一条能读懂的理由。
      if (typeof createdAt !== 'string' || !createdAt) {
        corrupt.push({ path: entry, reason: '清单里没有 createdAt' })
        continue
      }
      dated.push({ path: entry, createdAt })
    } catch (err) {
      corrupt.push({ path: entry, reason: String(err.message ?? err) })
    }
  }

  dated.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  return { dir: dated.length ? dated[0].path : null, corrupt }
}

// ── zip ─────────────────────────────────────────────────────────
//
// 【为什么要自己写 zip】启动包要求是 .zip（issue #73），而少一个依赖比多一个
// 重要：node 没有内置的 zip，`zip` 命令在 Windows 上不一定有（Git Bash 通常
// 不带），而 Windows 自带的 bsdtar 能写 zip、Linux 的 GNU tar 不能——三平台
// 找不到一个共同的命令。仓库里已经有"不依赖 gh、不装额外工具"的先例
//（见 scripts/release.mjs 的文件头），这里沿用。
//
// 【为什么只用 store（不压缩）】包里体积的 99% 是 docker save 出来的
// images/*.tar.gz——它已经压缩过了，再 deflate 一遍是纯浪费。为了剩下的
// 几个文本文件引第二条代码路径（method=8 + 压缩/未压缩两套尺寸字段）不值。
// 于是整包 store：实现只有"写头部 + 拼字节"这一段，没有分支可写错。
//
// 【Node 22 自带 crc32】`zlib.crc32`（Node ≥ 22.2）。自己查表算 CRC 是一段
// 容易写错又难自证的代码——这里不必写。

const ZIP_LOCAL_SIG = 0x04034b50
const ZIP_CENTRAL_SIG = 0x02014b50
const ZIP_EOCD_SIG = 0x06054b50

/** 把 Date 转成 MS-DOS 的 (time, date) 两个 16 位字段。zip 的时间格式就是这个。 */
function dosTimestamp(d) {
  const time = (d.getHours() << 11) | (d.getMinutes() << 5) | Math.floor(d.getSeconds() / 2)
  const date = ((d.getFullYear() - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate()
  return { time: time & 0xffff, date: date & 0xffff }
}

/**
 * 打一个 zip。
 *
 * @param {string} outPath 写到哪里
 * @param {{name: string, path: string}[]} entries name 是包内路径（用 / 分隔）
 * @returns {{bytes: number, count: number}}
 */
export function writeZip(outPath, entries) {
  // 【64K 个文件 / 4 GiB 的上限要显式挡】不挡的话超限会写出一个**看起来正常、
  // 解压时报错**的包——而那是给用户的东西，失败发生在他们那边。
  if (entries.length > 0xffff) {
    fail(`zip 里超过 65535 个文件（${entries.length} 个），这个简易实现不支持 zip64。`)
  }

  const now = new Date()
  const stamp = dosTimestamp(now)
  const parts = []
  const central = []
  let offset = 0

  for (const e of entries) {
    const data = readFileSync(e.path)
    const nameBuf = Buffer.from(e.name, 'utf8')

    const local = Buffer.alloc(30)
    local.writeUInt32LE(ZIP_LOCAL_SIG, 0)
    local.writeUInt16LE(20, 4) // version needed to extract
    local.writeUInt16LE(0, 6) // general purpose flags
    local.writeUInt16LE(0, 8) // method = 0（store）
    local.writeUInt16LE(stamp.time, 10)
    local.writeUInt16LE(stamp.date, 12)
    local.writeUInt32LE(crc32(data) >>> 0, 14)
    local.writeUInt32LE(data.length, 18) // compressed size（store 时两者相同）
    local.writeUInt32LE(data.length, 22) // uncompressed size
    local.writeUInt16LE(nameBuf.length, 26)
    local.writeUInt16LE(0, 28) // extra length

    const cd = Buffer.alloc(46)
    cd.writeUInt32LE(ZIP_CENTRAL_SIG, 0)
    cd.writeUInt16LE(20, 4) // version made by
    cd.writeUInt16LE(20, 6) // version needed
    cd.writeUInt16LE(0, 8)
    cd.writeUInt16LE(0, 10)
    cd.writeUInt16LE(stamp.time, 12)
    cd.writeUInt16LE(stamp.date, 14)
    cd.writeUInt32LE(crc32(data) >>> 0, 16)
    cd.writeUInt32LE(data.length, 20)
    cd.writeUInt32LE(data.length, 24)
    cd.writeUInt16LE(nameBuf.length, 28)
    cd.writeUInt16LE(0, 30) // extra
    cd.writeUInt16LE(0, 32) // comment
    cd.writeUInt16LE(0, 34) // disk number start
    cd.writeUInt16LE(0, 36) // internal attributes
    cd.writeUInt32LE(0, 38) // external attributes
    cd.writeUInt32LE(offset, 42) // 本地头在这个包里的偏移

    parts.push(local, nameBuf, data)
    central.push(cd, nameBuf)
    offset += local.length + nameBuf.length + data.length

    if (offset > 0xffffffff) {
      fail('zip 超过 4 GiB，这个简易实现不支持 zip64。镜像 tarball 太大了？')
    }
  }

  const centralBuf = Buffer.concat(central)
  const eocd = Buffer.alloc(22)
  eocd.writeUInt32LE(ZIP_EOCD_SIG, 0)
  eocd.writeUInt16LE(0, 4) // 本磁盘号
  eocd.writeUInt16LE(0, 6) // 中央目录起始磁盘号
  eocd.writeUInt16LE(entries.length, 8)
  eocd.writeUInt16LE(entries.length, 10)
  eocd.writeUInt32LE(centralBuf.length, 12)
  eocd.writeUInt32LE(offset, 16) // 中央目录的偏移
  eocd.writeUInt16LE(0, 20) // 注释长度

  writeFileSync(outPath, Buffer.concat([...parts, centralBuf, eocd]))
  return { bytes: offset + centralBuf.length + eocd.length, count: entries.length }
}
