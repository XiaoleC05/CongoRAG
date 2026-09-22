#!/usr/bin/env node
/**
 * 回滚脚本（issue #74）。
 *
 * 把 upgrade.mjs 备份下来的那一份还原回去：数据库 dump 还原 + 数据卷还原 +
 * 镜像 tag 退回去。三件事必须一起做——只还原数据库会得到"文档记录指向
 * 已经不存在的原始文件"，只退镜像 tag 会得到"旧二进制对着新 schema"。
 *
 * 【这个脚本会销毁当前数据，所以它要求 --yes】还原数据库用的是
 * DROP DATABASE + CREATE DATABASE + pg_restore，不是"覆盖一下"：
 * 迁移之后新产生的数据（新会话、新文档、新向量）会被丢掉。这是回滚的
 * 定义，不是缺陷——但它必须是一个显式的动作，不能因为少打一个参数就发生。
 *
 * 用法：
 *   node scripts/rollback.mjs --dir deployments/startup --dry-run
 *   node scripts/rollback.mjs --dir deployments/startup          # 取最新那份备份
 *   node scripts/rollback.mjs --dir deployments/startup --backup deployments/startup/backups/2026-09-22T10-00-00 --yes
 */

import { existsSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

import {
  composer,
  defaultBackupRoot,
  fail,
  imageExists,
  imageOf,
  info,
  ok,
  pgSqlOn,
  pickNewestBackup,
  readEnvFile,
  run,
  runWithInput,
  setEnvVar,
  sha256File,
  step,
  tryRun,
} from './delivery-lib.mjs'

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
      '回滚 ConGoRAG 启动包：还原数据库 + 还原数据卷 + 退回镜像 tag。',
      '',
      '  --dir 目录        含 docker-compose.yml 与 .env 的目录（默认 "."）',
      '  --backup 目录     要还原哪一份备份（默认 backups/ 下最新的那份）',
      '  --pg 容器名       PostgreSQL 容器名（默认 congorag-postgres）',
      '  --api 容器名      api 容器名（默认 congorag-api）',
      '  --archive-image   解包数据卷用的镜像（默认 alpine:3.22）',
      '  --dry-run         只打印将要做什么，不动任何数据',
      '  --yes             确认销毁当前数据（不带它就只做检查）',
      '',
      '【它会丢数据】数据库是 DROP + CREATE + 还原，迁移之后新产生的数据会消失。',
      '用户视角的升级/回滚步骤见 docs/upgrading.md。',
    ].join('\n'),
  )
  process.exit(0)
}

const dir = resolve(argValue('--dir', '.'))
const pgContainer = argValue('--pg', 'congorag-postgres')
const apiContainer = argValue('--api', 'congorag-api')
const archiveImage = argValue('--archive-image', 'alpine:3.22')
const dryRun = hasFlag('--dry-run')
const confirmed = hasFlag('--yes')

const compose = composer(dir)

// ── 0. 找备份、校验备份 ─────────────────────────────────────────

step('找备份')

if (!existsSync(compose.composeFile)) {
  fail(`找不到 ${compose.composeFile}。用 --dir 指到启动包解压出来的那个目录。`)
}

const backupRoot = defaultBackupRoot(dir)
// 【不能写成 `resolve(argValue(...)) || pickNewestBackup(...)`】resolve('')
// 返回的是当前工作目录——一个真值。那样写的话"没传 --backup"会静默地
// 变成"用当前目录当备份"，然后在找不到 manifest.json 时给出一个和真实
// 原因无关的报错。先判空，再 resolve。
const backupArg = argValue('--backup', '')
let backupDir
if (backupArg) {
  // 用户指了哪一份就是哪一份。就算它的清单读不出来，也该由下面的清单校验
  // 给出针对**这一份**的报错，而不是在这里被"挑最新"的逻辑拦下来。
  backupDir = resolve(backupArg)
} else {
  const picked = pickNewestBackup(backupRoot)
  // 【清单损坏时必须拦下来，不能跳过它去选更旧的一份】回滚是最后一次机会：
  // 悄悄退回到一份更旧的备份，用户以为在还原 A、实际还原了 B，比直接报错
  // 糟得多。原来的写法是在 sort 的比较器里 JSON.parse——目录下任何一份清单
  // 坏掉都会以解析异常崩在"选备份"这一步，看不出是哪个文件、也不知道还能
  // 怎么办。这里把文件、原因、两条出路一起给出来。
  if (picked.corrupt.length) {
    fail(
      `备份目录里有读不出清单的备份，不能替你在它们之间做选择：\n` +
        picked.corrupt.map((c) => `  · ${c.path}\n    ${c.reason}`).join('\n') +
        '\n\n  修好或删掉上面这些目录，或者用 --backup 明确指定要还原哪一份：\n' +
        `    node scripts/rollback.mjs --dir ${dir} --backup <备份目录> --yes`,
    )
  }
  backupDir = picked.dir
}
if (!backupDir || !existsSync(backupDir)) {
  fail(
    `找不到备份。\n` +
      `  默认去 ${backupRoot} 里找最新的一份；一份都没有的话，说明还没升级过，\n` +
      '  或者备份被删了。用 --backup 显式指定路径。',
  )
}
info(`备份目录：${backupDir}`)

const manifestPath = join(backupDir, 'manifest.json')
if (!existsSync(manifestPath)) {
  fail(
    `${manifestPath} 不存在——这不是本项目产出的备份目录。\n` +
      '  没有清单就无法知道这份备份来自哪个版本、对应哪个卷，回滚会靠猜。',
  )
}
// 【清单读不出来要停在"能读懂这句话"的地方】文件在、但内容坏了（写了一半、
// 被同步盘截断、手工编辑出错）时，裸的 JSON.parse 会抛一个带 node 内部路径的
// SyntaxError 栈——用户看到的是 `file:///.../rollback.mjs:110:23`，而不是
// "这份备份的清单坏了、你该换哪一份"。上面那条选备份的报错会把人指到 --backup，
// 所以这条路径同样不能是一个未捕获的异常。
let manifest
try {
  manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
} catch (err) {
  fail(
    `${manifestPath} 读不出来：${String(err.message ?? err)}\n` +
      '  这份备份的清单已经损坏，回滚中止（**还没有动任何数据**）。\n' +
      '  换一份备份：--backup <备份目录>；确定这份没用了就把它删掉。',
  )
}
ok(`清单：${manifest.fromVersion} → ${manifest.toVersion}（备份于 ${manifest.createdAt}）`)

// 【校验哈希，不是"文件在就行"】备份是在灾难发生**之前**做的，所以它有
// 整整一段时间可以被各种东西损坏（磁盘、同步盘、手工挪动、tc 拷贝中断）。
// 回滚是最后一次机会——这时才发现备份是坏的，等于没有备份。校验一次哈希
// 的成本是几秒，换到的是"这份备份确实还是当初那一份"。
for (const [label, entry] of [
  ['数据库 dump', manifest.database],
  ['数据卷归档', manifest.dataVolume],
]) {
  const p = join(backupDir, entry.file)
  if (!existsSync(p)) fail(`${label} 不存在：${p}`)
  const size = statSync(p).size
  if (size !== entry.bytes) {
    fail(`${label} 的大小和清单对不上（清单 ${entry.bytes}，实际 ${size}）。备份已被改动，回滚中止。`)
  }
  const actual = sha256File(p)
  if (actual !== entry.sha256) {
    fail(`${label} 的 sha256 和清单对不上。\n  清单 ${entry.sha256}\n  实际 ${actual}\n  备份已损坏，回滚中止——用另一份备份。`)
  }
  ok(`${label} 校验通过（${(size / 1024 / 1024).toFixed(2)} MiB）`)
}

// ── 1. 前置检查 ─────────────────────────────────────────────────

step('前置检查')

const envPath = join(dir, '.env')
if (!existsSync(envPath)) fail(`找不到 ${envPath}。回滚要改里面的 CONGORAG_VERSION。`)
info(`.env：${envPath}`)

const toImage = `congorag-api:${manifest.fromVersion}`
if (!imageExists(toImage)) {
  fail(
    `本地没有 ${toImage}——但回滚要退回的就是它。\n` +
      '  启动包是离线分发的：把升级时用的那份旧 tarball 重新 `docker load` 进来，\n' +
      '  `docker images | grep congorag` 能看到导进来的 tag。',
  )
}
ok(`要退回的镜像在本地：${toImage}`)

const currentImage = imageOf(apiContainer)
info(`api 现在跑的是：${currentImage ?? '（容器不在）'}`)

if (tryRun('docker', ['inspect', '-f', '{{.State.Running}}', pgContainer]) !== 'true') {
  fail(`容器 ${pgContainer} 不在跑。回滚要先有库可还原，先 docker compose up -d postgres。`)
}
ok(`${pgContainer} 在跑`)

const volumeName = manifest.volumeName
if (!volumeName) fail('清单里没有卷名（这份备份是早期版本写的）。回滚中止。')

// 【dry-run 排在 --yes 检查之前】两者同时给时以 dry-run 为准：一个说
// "别动"，一个说"动手"。安全的那一个赢，不用额外解释。
if (dryRun) {
  console.log('\n=== dry-run：将要执行 ===\n')
  console.log('  docker compose stop api worker')
  console.log(`  docker exec ${pgContainer} psql -U postgres -d postgres -c "DROP DATABASE IF EXISTS congorag" ...`)
  console.log(`  docker exec -i ${pgContainer} pg_restore ... < ${join(backupDir, manifest.database.file)}`)
  console.log(`  docker run --rm -v ${volumeName}:/data -v ${backupDir}:/backup ${archiveImage} sh -c '<清空 /data 并解包>'`)
  console.log(`  .env: CONGORAG_VERSION=${manifest.fromVersion}`)
  console.log('  docker compose up -d --wait')
  console.log('\n（dry-run 到此为止，什么都没做）')
  process.exit(0)
}

if (!confirmed) {
  console.log('\n════════ 检查全部通过，但还没有动任何数据 ════════\n')
  console.log('回滚会做三件事，其中第一件**会丢掉迁移之后新产生的数据**：')
  console.log(`  1. DROP DATABASE congorag → CREATE → 用 ${manifest.database.file} 还原`)
  console.log(`     （清单里的库有 ${manifest.database.tables} 张表，时间是 ${manifest.createdAt}）`)
  console.log(`  2. 清空卷 ${volumeName} 并解包 ${manifest.dataVolume.file}`)
  console.log(`  3. 把 .env 的 CONGORAG_VERSION 从 ${manifest.toVersion} 改回 ${manifest.fromVersion}`)
  console.log('\n  确认要这么做就加 --yes 重新跑；只看计划就加 --dry-run。')
  process.exit(0)
}

// ── 2. 停应用 ───────────────────────────────────────────────────
//
// 【必须停干净】api / worker 都连着库，DROP DATABASE 会因为"还有活动连接"
// 失败；同时它们还握着 /data 卷里的文件句柄，清空卷时可能删不掉。
// 先停再动，顺序不能反。

step('停掉 api 与 worker')
try {
  compose.run(['stop', 'api', 'worker'])
} catch (err) {
  fail(`停容器失败：${String(err.stderr ?? err.message).trim()}`)
}
ok('已停（postgres 留着不动——要往它里面还原）')

// ── 3. 还原数据库 ───────────────────────────────────────────────

step('还原数据库')

try {
  // 【先掐掉残留连接】DROP DATABASE 只要有**任何一个**活动连接就会失败，
  // 而失败信息是 "database congorag is being accessed by other users"——
  // 它不会告诉你是谁。除了 api/worker，还可能有：用户自己开的 psql、
  // 之前 `docker compose exec` 留下的会话、连接池里还没超时的那几条。
  pgSqlOn(
    pgContainer,
    'postgres',
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'congorag' AND pid <> pg_backend_pid()",
  )
  pgSqlOn(pgContainer, 'postgres', 'DROP DATABASE IF EXISTS congorag')
  pgSqlOn(pgContainer, 'postgres', 'CREATE DATABASE congorag')
} catch (err) {
  fail(
    `重建数据库失败：${String(err.stderr ?? err.message).trim()}\n` +
      '  此时库还在（DROP/CREATE 没成功），api 与 worker 是停的。\n' +
      '  确认没有别的客户端连着 congorag 之后再重跑本脚本。',
  )
}
ok('congorag 库已重建（空的）')

const dumpBytes = readFileSync(join(backupDir, manifest.database.file))
try {
  // --no-owner：dump 里的对象属主是 postgres，还原用户也是 postgres，
  // 正常情况下不需要它；带上是为了让"目标环境里的角色名不一样"不至于
  // 把一次还原变成一串权限错误。--role=postgres 保证还原期间的身份确定。
  runWithInput(
    'docker',
    ['exec', '-i', pgContainer, 'pg_restore', '-U', 'postgres', '-d', 'congorag', '--no-owner', '--role=postgres'],
    dumpBytes,
  )
} catch (err) {
  fail(
    `pg_restore 失败：${String(err.stderr ?? err.message).trim()}\n` +
      '  库现在处于**部分还原**的状态，不要就这么把它当成好的。\n' +
      '  再跑一次本脚本（会重新 DROP/CREATE）通常就够了。',
  )
}
ok('数据库已还原')

// ── 4. 还原数据卷 ───────────────────────────────────────────────

step(`还原数据卷 ${volumeName}`)

const tarName = manifest.dataVolume.file
try {
  // 【一条 sh 里做两件事，中间不落空】清空 + 解包必须连着做：分两条命令的话，
  // 中间失败会留下一个"空卷"，而那一刻用户看到的现象（文档全没了）和
  // "回滚失败"完全一样——但实际上它只是还没解包，是可以救的。
  // 放在同一条 sh 的 && 里，失败时用户至少知道"卷是空的，重跑一次"。
  //
  // 【通配符覆盖点开头的文件】/data 里有 .gitkeep 之类的隐藏文件吗？没有，
  // 但 master.key 未来可能被改成点开头的名字，而且 rm 的 * 不匹配点开头是
  // 一个经典的坑——多写两个模式让它不可能漏。
  run('docker', [
    'run', '--rm',
    '-v', `${volumeName}:/data`,
    '-v', `${backupDir}:/backup`,
    archiveImage,
    'sh', '-c',
    `rm -rf /data/* /data/.[!.]* /data/..?* 2>/dev/null; tar xzf /backup/${tarName} -C /data`,
  ])
} catch (err) {
  fail(
    `还原数据卷失败：${String(err.stderr ?? err.message).trim()}\n` +
      `  数据库已经还原好了，卷可能只解了一半。重跑本脚本即可（会重新 DROP/CREATE + 重解包）。`,
  )
}
ok('数据卷已还原')

// ── 5. 退回镜像 tag ─────────────────────────────────────────────

step('退回镜像 tag')
const env = readEnvFile(envPath)
const currentVersion = env.CONGORAG_VERSION
setEnvVar(envPath, 'CONGORAG_VERSION', manifest.fromVersion)
ok(`.env: CONGORAG_VERSION ${currentVersion} → ${manifest.fromVersion}`)

try {
  compose.run(['up', '-d', '--wait', '--wait-timeout', '180'])
} catch (err) {
  fail(
    `数据已经还原好了，但容器没起来：\n  ${String(err.stderr ?? err.message).trim()}\n\n` +
      '  数据和镜像 tag 都已经退回去了，所以现在重跑一次 `docker compose up -d` 大概率就好。\n' +
      '  还不行的话看 `docker compose logs api`。',
  )
}

console.log('\n════════ 回滚完成 ════════')
console.log(`  版本        ${currentVersion} → ${manifest.fromVersion}`)
console.log(`  数据库      已从 ${manifest.createdAt} 的备份还原`)
console.log(`  数据卷      已还原（${volumeName}）`)
console.log('')
console.log('  打开界面确认数据回来了。')
console.log('  【提醒】这次回滚丢掉了迁移之后新产生的数据——那是回滚的定义。')
