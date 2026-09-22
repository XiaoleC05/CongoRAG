#!/usr/bin/env node
/**
 * 升级脚本（issue #74）。
 *
 * 做的事，按顺序，**顺序本身就是这个脚本存在的理由**：
 *
 *   1. 备份数据库（pg_dump 走容器，宿主机上没有这个命令）
 *   2. 备份数据卷（原始文件 + 主密钥 + tiktoken 缓存）
 *   3. **校验备份真的可用**（文件在、是 pg_dump 格式、哈希对得上）
 *   4. 只有 1–3 全部成功，才跑迁移
 *   5. 拉/起新镜像
 *
 * 任一步失败即以非零退出并说明"已经做到哪一步、还没做什么"。
 *
 * 【为什么顺序必须由东西保证，而不是靠文档】这个项目里升级不是可选动作：
 * v3.0 的 CHANGELOG 写明「升级前必须先跑 make migrate-up」——漏跑 0006 的
 * 直接后果是聊天整条路径 500。而迁移恰恰是最容易丢数据的时刻，所以
 * "备份成功才迁移"不能是一条写给人看的注意事项：人会累、会跳步骤，
 * 而脚本不会。§11 的陷阱表第一条就是「备份失败仍继续迁移 | 升级失败没退路」。
 *
 * 【为什么要备份数据卷，而不是只备份 data/documents】issue #74 的原文写的是
 * "另加 data/documents 打包"，这里扩展到整个 /data 卷，理由是那个卷里还有
 * **主密钥**（/data/master.key）。库里所有 provider 的 api key 都是用主密钥
 * 加密的，主密钥丢了的话，恢复出来的数据库里那些 key 全部解不开——
 * 表现为"数据都在，但每个模型都报认证失败"。tiktoken 缓存也在这个卷里，
 * 它是可再生的，但一并带走不花什么代价。见 docs/upgrading.md。
 *
 * 用法：
 *   node scripts/upgrade.mjs --dir deployments/startup --dry-run
 *   node scripts/upgrade.mjs --dir deployments/startup
 *   node scripts/upgrade.mjs            # --dir 默认 "."，即在启动包目录里跑
 */

import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

import {
  composer,
  defaultBackupRoot,
  ensureDir,
  fail,
  imageExists,
  imageOf,
  info,
  ok,
  pgSql,
  readEnvFile,
  run,
  runBinary,
  sha256File,
  step,
  tryRun,
  volumeNameOf,
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
      '升级 ConGoRAG 启动包：先备份、校验通过才迁移、任一步失败即中止。',
      '',
      '  --dir 目录        含 docker-compose.yml 与 .env 的目录（默认 "."）',
      '  --to 版本         目标镜像 tag。默认取 .env 里的 CONGORAG_VERSION。',
      '  --backup-dir 目录 备份写到哪里（默认 <dir>/backups/<时间戳>）',
      '  --pg 容器名       PostgreSQL 容器名（默认 congorag-postgres）',
      '  --api 容器名      api 容器名（默认 congorag-api，用来读"当前版本"）',
      '  --archive-image   打卷备份用的镜像（默认 alpine:3.22）',
      '  --dry-run         只打印将要做什么，不备份、不迁移、不动容器',
      '',
      '升级流程（用户视角）见 docs/upgrading.md。',
    ].join('\n'),
  )
  process.exit(0)
}

const dir = resolve(argValue('--dir', '.'))
const pgContainer = argValue('--pg', 'congorag-postgres')
const apiContainer = argValue('--api', 'congorag-api')
const archiveImage = argValue('--archive-image', 'alpine:3.22')
const dryRun = hasFlag('--dry-run')

const compose = composer(dir)

// ── 0. 前置检查 ─────────────────────────────────────────────────

step('前置检查')

if (!existsSync(compose.composeFile)) {
  fail(`找不到 ${compose.composeFile}。用 --dir 指到启动包解压出来的那个目录（里面有 docker-compose.yml 和 .env）。`)
}
const envPath = join(dir, '.env')
if (!existsSync(envPath)) {
  fail(
    `找不到 ${envPath}。\n` +
      '  先 `cp .env.example .env` 并把它改对——compose 与这个脚本都读它，\n' +
      '  缺了它连"要升到哪个版本"都无从得知。',
  )
}
ok('compose 文件与 .env 都在')

const env = readEnvFile(envPath)
const toVersion = argValue('--to', env.CONGORAG_VERSION)
if (!toVersion) {
  fail(`${envPath} 里没有 CONGORAG_VERSION，也没有用 --to 指定目标版本。`)
}

// 【"当前版本"必须从正在跑的容器读，不能从 .env 读】用户升级时**已经**
// 把 .env 改成新版本号了，.env 描述的是"将要是什么"，不是"现在是什么"。
// 备份清单里记的必须是后者——回滚时要回到的就是它。
const runningImage = imageOf(apiContainer)
if (!runningImage) {
  fail(
    `读不到容器 ${apiContainer} 的镜像。\n` +
      '  升级的前提是这套环境**正在跑**（要先有东西可备份）。\n' +
      '  没起的话先 `docker compose up -d`；容器名不是默认的就用 --api 指定。',
  )
}
const fromVersion = runningImage.includes(':') ? runningImage.split(':').pop() : runningImage
info(`当前在跑：${runningImage}`)
info(`目标版本：congorag-api:${toVersion}`)
if (fromVersion === toVersion) {
  info('【注意】两者相同——这会把迁移与备份重跑一遍，但不会换镜像。')
  info('        真要升级的话，先把 .env 里的 CONGORAG_VERSION 改成新版本号。')
}

// 目标镜像必须在本地。启动包是 docker save / docker load 分发的，
// 没有 registry 可拉——镜像不在本地时 compose 会以 "image not found" 失败，
// 而那句话不会告诉你"你还没 docker load"。
for (const name of ['api', 'worker', 'migrate']) {
  const ref = `congorag-${name}:${toVersion}`
  if (!imageExists(ref)) {
    fail(
      `本地没有镜像 ${ref}。\n` +
        `  启动包是离线分发的：先 docker load -i images/congorag-images-linux-<架构>.tar.gz\n` +
        '  （架构按 `docker info --format "{{.Architecture}}"` 判断）。\n' +
        '  已经 load 过的话，用 `docker images | grep congorag` 看看实际导进来的 tag 是什么。',
    )
  }
}
ok('三个目标镜像都在本地')

// postgres 必须在跑，否则备份无从谈起
if (tryRun('docker', ['inspect', '-f', '{{.State.Running}}', pgContainer]) !== 'true') {
  fail(`容器 ${pgContainer} 不在跑，没有东西可备份。先 docker compose up -d。`)
}
ok(`${pgContainer} 在跑`)

const volumeName = volumeNameOf(apiContainer, '/data')
if (!volumeName) {
  fail(
    `读不到容器 ${apiContainer} 挂到 /data 的卷名。\n` +
      '  数据卷（原始文件 + 主密钥）是备份的一半，读不到它就不能继续——\n' +
      '  只备份数据库会导致文档记录和原始文件对不上（§11 陷阱表第二条）。',
  )
}
info(`数据卷：${volumeName}`)

const backupDir = resolve(argValue('--backup-dir', join(defaultBackupRoot(dir), timestamp())))
info(`备份目录：${backupDir}`)

if (dryRun) {
  console.log('\n=== dry-run：将要执行 ===\n')
  console.log(`  1. docker exec ${pgContainer} pg_dump -U postgres -d congorag -Fc > ${join(backupDir, 'database.dump')}`)
  console.log(`  2. docker run --rm -v ${volumeName}:/data:ro -v ${backupDir}:/backup ${archiveImage} tar czf /backup/data-volume.tar.gz -C /data .`)
  console.log('  3. 校验：文件存在、pg_dump 魔数、sha256 写进 manifest.json')
  console.log(`  4. docker compose -f ${compose.composeFile} run --rm migrate`)
  console.log('  5. docker compose up -d --wait')
  console.log(`\n  回滚：node scripts/rollback.mjs --dir ${dir} --backup ${backupDir}`)
  console.log('\n（dry-run 到此为止，什么都没做）')
  process.exit(0)
}

// ── 1. 备份数据库 ───────────────────────────────────────────────
//
// 【为什么走容器】宿主机上没有 psql / pg_dump（项目的部署边界就是"只装
// Docker"）。issue #74 的原文在这条上专门加了括号："pg_dump 不在 PATH，走容器"。
//
// 【为什么用 -Fc（自定义格式）而不是纯 SQL】自定义格式带完整性校验、支持
// pg_restore 的选择性恢复，而且**压缩过**——纯 SQL 的 dump 里那几张向量表
// 会把体积撑得很大。代价是它不能直接拿文本编辑器看，需要 pg_restore -l。
//
// 【为什么要缓存整个 Buffer 而不是流式写文件】-Fc 的输出必须完整才能校验
// 魔数与哈希；流到一半失败的话，落盘的会是一个看起来正常、实际截断的文件——
// 那比没有备份更危险，因为它会让人以为有退路。缓存的代价是一个几百 MB 的
// Buffer，对本项目面向的个人知识库规模是可接受的。

step('备份数据库')

ensureDir(backupDir)
const dumpPath = join(backupDir, 'database.dump')

let dumpBytes
try {
  dumpBytes = runBinary('docker', [
    'exec', pgContainer,
    'pg_dump', '-U', 'postgres', '-d', 'congorag', '-Fc',
  ])
} catch (err) {
  fail(
    `pg_dump 失败，升级中止（**没有跑迁移、没有动容器**）：\n` +
      `  ${String(err.stderr ?? err.message).trim()}`,
  )
}

// 校验 1：pg_dump 的自定义格式以 "PGDMP" 开头。空文件、HTML 错误页、
// 半截输出都过不了这一关——**而且这一关是在写盘之前过的**。
if (dumpBytes.length < 5 || dumpBytes.subarray(0, 5).toString('latin1') !== 'PGDMP') {
  fail(
    `pg_dump 的输出不是自定义格式（前 5 字节不是 PGDMP，长度 ${dumpBytes.length}）。\n` +
      '  最常见的两个原因：容器里连的是另一个库，或者 pg_dump 因为权限/连接数\n' +
      '  限制写到一半就退出了。升级中止，什么都没改。',
  )
}

writeFileSync(dumpPath, dumpBytes)
ok(`database.dump  ${(dumpBytes.length / 1024 / 1024).toFixed(2)} MiB`)

// 校验 2：一行数据到底有没有。空库 dump 出 5 字节也是合法的 PGDMP，
// 而"备份了一个空库"和"备份失败"在用户眼里是同一件事。
const tableCount = Number(
  pgSql(pgContainer, "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'"),
)
if (!Number.isFinite(tableCount) || tableCount === 0) {
  fail('数据库里一张表都没有。这个库看起来不是 ConGoRAG 的库，升级中止（什么都没改）。')
}
ok(`库里 ${tableCount} 张表`)

// ── 2. 备份数据卷 ───────────────────────────────────────────────

step('备份数据卷（原始文件 + 主密钥 + tiktoken 缓存）')

const volumeTar = join(backupDir, 'data-volume.tar.gz')
// 【挂 :ro】源卷只读。这个脚本没有理由写它，只读挂载让"写坏源数据"
// 从"可能"变成"不可能"——而这一步的全部意义就是留退路。
try {
  run('docker', [
    'run', '--rm',
    '-v', `${volumeName}:/data:ro`,
    '-v', `${backupDir}:/backup`,
    archiveImage,
    'tar', 'czf', '/backup/data-volume.tar.gz', '-C', '/data', '.',
  ])
} catch (err) {
  fail(
    `打包数据卷失败，升级中止（**没有跑迁移、没有动容器**）：\n` +
      `  ${String(err.stderr ?? err.message).trim()}\n` +
      `  这一条需要本地有 ${archiveImage}（首次会自动拉）。没有外网的话用 --archive-image 指定一个已有的。`,
  )
}
if (!existsSync(volumeTar) || !sha256File(volumeTar)) {
  fail('数据卷归档没写出来。升级中止，什么都没改。')
}
const volumeBytes = readFileSync(volumeTar).length
ok(`data-volume.tar.gz  ${(volumeBytes / 1024 / 1024).toFixed(2)} MiB`)

// 校验 3：把 manifest 写下来。它同时是**给回滚脚本读的输入**：
// fromVersion 是回滚要退回到的那个 tag，volumeName 是还原时要往哪个卷里灌。
const manifest = {
  createdAt: new Date().toISOString(),
  fromVersion,
  fromImage: runningImage,
  toVersion,
  composeFile: compose.composeFile,
  pgContainer,
  apiContainer,
  volumeName,
  database: { file: 'database.dump', bytes: dumpBytes.length, sha256: sha256File(dumpPath), tables: tableCount },
  dataVolume: { file: 'data-volume.tar.gz', bytes: volumeBytes, sha256: sha256File(volumeTar) },
  // 【迁移有没有跑过，必须记在清单里】回滚脚本要据此判断"这份备份
  // 对应的是迁移前还是迁移后的库"。备份一定是迁移前做的，但清单写下来
  // 之后才有东西可断言——不然这个事实只活在脚本的执行顺序里。
  migrated: false,
}
const manifestPath = join(backupDir, 'manifest.json')
writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, 'utf8')
ok(`manifest.json  已写入`)

console.log(`\n备份完成：${backupDir}`)
console.log('备份已校验通过——现在可以安全地跑迁移了。')

// ── 3. 迁移（备份成功之后才走到这里）────────────────────────────

step(`跑迁移（congorag-migrate:${toVersion}）`)
try {
  // 迁移容器自己会打印两套迁移的结果（它的入口脚本见
  // deployments/docker/migrate-entrypoint.sh），这里只原样转发，
  // 不再复述一遍——复述会让"跑完了"在输出里出现两次，看起来像跑了两遍。
  const out = compose.run(['run', '--rm', 'migrate'])
  if (out) console.log(out)
} catch (err) {
  fail(
    '迁移失败。\n' +
      `  备份在 ${backupDir}（内容是有效的，没有受影响）。\n` +
      '  容器还没有换镜像，所以现在这套环境仍然是升级前的状态。\n' +
      '  排查完再重跑本脚本即可——迁移是幂等的，备份会重新做一份。\n\n' +
      `  ${String(err.stderr ?? err.message).trim()}`,
  )
}
// 不再补一句"两套迁移都跑完了"：迁移容器已经自己说过一次（那是它的入口
// 脚本的最后一行），重复一遍会让输出里出现两句一模一样的话。
info('迁移容器退出码为 0')

// ── 4. 起新镜像 ─────────────────────────────────────────────────

step('切换到新镜像')
manifest.migrated = true
writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, 'utf8')

try {
  // --wait 会等到每个服务 healthy（或 running）。没有它的话，一个起不来的
  // api 会被"成功"掩盖，而用户要到打开浏览器时才发现。
  compose.run(['up', '-d', '--wait', '--wait-timeout', '180'])
} catch (err) {
  fail(
    `迁移已经跑完，但新镜像没起来：\n  ${String(err.stderr ?? err.message).trim()}\n\n` +
      `  现在库是新 schema、容器是旧镜像——**这个状态不一致，建议回滚**：\n` +
      `    node scripts/rollback.mjs --dir ${dir} --backup ${backupDir}`,
  )
}

const nowImage = imageOf(apiContainer)
ok(`api 现在跑的是 ${nowImage}`)

// ── 5. 报告 ─────────────────────────────────────────────────────

console.log('\n════════ 升级完成 ════════')
console.log(`  版本        ${fromVersion} → ${toVersion}`)
console.log(`  备份        ${backupDir}`)
console.log(`  数据库      database.dump（${tableCount} 张表）`)
console.log(`  数据卷      data-volume.tar.gz（${volumeName}）`)
console.log('')
console.log('  打开 http://127.0.0.1:' + (env.CONGORAG_PORT || '3210') + ' 确认界面与数据都在。')
console.log(`  确认没问题之前**不要删 ${backupDir}**。`)
console.log(`  要退回去：node scripts/rollback.mjs --dir ${dir} --backup ${backupDir}`)

function timestamp() {
  // 文件名里带上时间戳，秒级足够——同一个用户不会在同一秒里升级两次，
  // 而毫秒会让目录名长到没法在终端里一眼读出来。
  return new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)
}
