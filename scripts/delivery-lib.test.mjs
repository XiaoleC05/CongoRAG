// delivery-lib.test.mjs —— 交付形态共用工具的单元测试（issue #104）。
//
// 【为什么这一层值得单独测】upgrade.mjs 与 rollback.mjs 是仓库里代价最高的
// 两条路径（回滚会 DROP DATABASE + 清空 /data），而它们的执行机会**只有用户
// 的机器**：脚本被原样拷进启动包，跑错了没有第二次机会。这个文件只测
// delivery-lib 里"不碰 docker、不碰网络"的那部分——纯函数与读写文件的边界，
// 也就是能在 CI 上每次 push 都跑得起来的那一半。
//
// 里面几个常量（CRC32 / SHA-256）是**手写死的公开值**，不是用实现自己算出来的：
// 拿 crc32() 去验 writeZip 写的 crc32 字段是循环论证，写死的值才能独立指出
// "字节写反了"。

import { after, test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import {
  parseVolumeName,
  pickNewestBackup,
  readEnvFile,
  setEnvVar,
  sha256File,
  writeZip,
} from './delivery-lib.mjs'

const ZIP_LOCAL = 0x04034b50
const ZIP_CENTRAL = 0x02014b50
const ZIP_EOCD = 0x06054b50

const CRC32_HELLO = 0x3610a686
const CRC32_WORLD = 0x30cb1101
const SHA256_HELLO = '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824'

// 【用完就删】这个文件会铺几十个临时目录，跑一次全留在别人的 Temp 里不合适。
// 每个用例一个目录，注册到 after 里统一清理；删不掉也不影响结论。
const created = []
after(() => {
  for (const dir of created) rmSync(dir, { recursive: true, force: true })
})

function scratch() {
  const dir = mkdtempSync(join(tmpdir(), 'congorag-delivery-'))
  created.push(dir)
  return dir
}

// ── zip ─────────────────────────────────────────────────────────

/**
 * 把 writeZip 产出的包读回来。
 *
 * 【为什么要自己写一个读的】"写进去的字节能读回原来的内容"才是这个函数唯一
 * 的验收标准，而只断言"文件非空"或"魔数对"都放得过一个偏移量写错的包。
 * 这里按真正的解压器那样走：从 EOCD 拿中央目录 → 从中央目录的偏移找本地头
 * → 按本地头的长度字段取数据。偏移量算错一步就取不到数据。
 *
 * 只支持 store（method 0）——那正是实现的全部能力，不是简化。
 */
function readStoreZip(buf) {
  const eocd = buf.subarray(buf.length - 22)
  assert.equal(eocd.readUInt32LE(0), ZIP_EOCD, '文件末尾 22 字节不是 EOCD')
  const count = eocd.readUInt16LE(10)

  const files = new Map()
  let p = eocd.readUInt32LE(16)
  for (let i = 0; i < count; i++) {
    assert.equal(buf.readUInt32LE(p), ZIP_CENTRAL, `第 ${i} 个中央目录项签名不对`)
    const nameLen = buf.readUInt16LE(p + 28)
    const name = buf.subarray(p + 46, p + 46 + nameLen).toString('utf8')
    const localAt = buf.readUInt32LE(p + 42)

    assert.equal(buf.readUInt32LE(localAt), ZIP_LOCAL, `${name} 的中央目录偏移指错了地方`)
    assert.equal(buf.readUInt16LE(localAt + 8), 0, `${name} 不是 store（method 0）`)
    const localNameLen = buf.readUInt16LE(localAt + 26)
    const extraLen = buf.readUInt16LE(localAt + 28)
    const size = buf.readUInt32LE(localAt + 22)
    assert.equal(buf.readUInt32LE(localAt + 18), size, `${name} 的两个长度字段不一致`)

    const at = localAt + 30 + localNameLen + extraLen
    files.set(name, buf.subarray(at, at + size))

    p += 46 + nameLen + buf.readUInt16LE(p + 30) + buf.readUInt16LE(p + 32)
  }
  return files
}

test('writeZip：字节能按 zip 格式读回来，内容和输入一致', () => {
  const dir = scratch()
  writeFileSync(join(dir, 'a.txt'), 'hello')
  writeFileSync(join(dir, 'b.txt'), 'world!!')

  const out = join(dir, 'out.zip')
  const result = writeZip(out, [
    { name: 'nested/a.txt', path: join(dir, 'a.txt') },
    { name: 'b.txt', path: join(dir, 'b.txt') },
  ])

  // 【返回的字节数必须就是落地文件的大小】清单里记的是这个数，算错了没人会发现。
  const buf = readFileSync(out)
  assert.equal(result.bytes, buf.length)
  assert.equal(result.count, 2)
  assert.equal(statSync(out).size, buf.length)

  const files = readStoreZip(buf)
  assert.deepEqual([...files.keys()], ['nested/a.txt', 'b.txt'], '包内路径（含目录）必须原样保留')
  assert.equal(files.get('nested/a.txt').toString('utf8'), 'hello')
  assert.equal(files.get('b.txt').toString('utf8'), 'world!!')

  // CRC 字段对着公开常量核一遍：写反了字节序或算错了多项式，这里会红。
  const first = buf.readUInt32LE(14)
  assert.equal(first, CRC32_HELLO)
  const second = 30 + 'nested/a.txt'.length + 'hello'.length
  assert.equal(buf.readUInt32LE(second + 14), CRC32_WORLD)

  // 中央目录的两条偏移必须指到两个本地头，而不是都指向开头。
  const offsets = [...files.keys()].map((n) => {
    let p = buf.length - 22
    p = buf.readUInt32LE(p + 16)
    for (let i = 0; i < 2; i++) {
      const nameLen = buf.readUInt16LE(p + 28)
      if (buf.subarray(p + 46, p + 46 + nameLen).toString('utf8') === n) return buf.readUInt32LE(p + 42)
      p += 46 + nameLen + buf.readUInt16LE(p + 30) + buf.readUInt16LE(p + 32)
    }
    return -1
  })
  assert.deepEqual(offsets, [0, second], '第二个条目的本地头必须跟在第一个后面，不是又指回 0')
})

test('writeZip：空包也是一个合法的 zip（只有 EOCD）', () => {
  const out = join(scratch(), 'empty.zip')
  const result = writeZip(out, [])
  assert.equal(result.count, 0)
  assert.equal(result.bytes, 22, '没有条目时只有 22 字节的 EOCD')
  assert.equal(readStoreZip(readFileSync(out)).size, 0)
})

test('writeZip：中文路径按 UTF-8 写，读回来不mojibake', () => {
  // 启动包里的文件名会是中文（文档名），而 zip 的 name 字段是字节串——
  // 按 latin1 或本地代码页写都会在这里炸。
  const dir = scratch()
  writeFileSync(join(dir, 'x'), '内容')
  const out = join(dir, 'cn.zip')
  writeZip(out, [{ name: '文档/说明.md', path: join(dir, 'x') }])
  const files = readStoreZip(readFileSync(out))
  assert.deepEqual([...files.keys()], ['文档/说明.md'])
})

// ── .env 读取与就地修改 ─────────────────────────────────────────

test('readEnvFile：文件不存在返回空对象，不抛', () => {
  assert.deepEqual(readEnvFile(join(scratch(), '没有这个文件')), {})
})

test('readEnvFile：忽略注释与空行，只按第一个 = 切分，两侧留白去掉', () => {
  const dir = scratch()
  const p = join(dir, '.env')
  writeFileSync(
    p,
    [
      '# 这是注释',
      '',
      '   ',
      'CONGORAG_VERSION=3.0',
      '  CONGORAG_DB_URL  =  postgres://u:p@h:5432/db  ',
      // 值里带 = 只切第一个：DB URL 的 query 参数就长这样，切错了整条连接串就废了
      'CONGORAG_EXTRA=a=b=c',
      '没有等号的一行',
      'CONGORAG_VERSION=3.1',
      '',
    ].join('\n'),
  )
  const env = readEnvFile(p)
  assert.equal(env.CONGORAG_VERSION, '3.1', '同一个键出现两次时后者覆盖前者')
  assert.equal(env.CONGORAG_DB_URL, 'postgres://u:p@h:5432/db')
  assert.equal(env.CONGORAG_EXTRA, 'a=b=c')
  assert.equal(Object.hasOwn(env, '没有等号的一行'), false)
})

test('readEnvFile：CRLF 的 .env 一样能读（Windows 上 edit 过的文件）', () => {
  const p = join(scratch(), '.env')
  writeFileSync(p, 'A=1\r\nB=2\r\n')
  assert.deepEqual(readEnvFile(p), { A: '1', B: '2' }, '\r 必须被去掉，否则值会带着 \\r')
})

test('readEnvFile：引号与 ${} 原样返回（刻意的限制，不是漏了展开）', () => {
  // 文件头注释里写明了不实现 dotenv 的引号剥离与变量展开。把这条钉住：
  // 哪天有人"顺手"加了半套解析，会在这里看到自己改的是契约。
  const p = join(scratch(), '.env')
  writeFileSync(p, 'QUOTED="a b"\nDOLLAR=${OTHER}\n')
  const env = readEnvFile(p)
  assert.equal(env.QUOTED, '"a b"')
  assert.equal(env.DOLLAR, '${OTHER}')
})

test('setEnvVar：就地改那一行，文件里其它内容一字不动', () => {
  const p = join(scratch(), '.env')
  const original = ['# 用户自己的笔记', 'CONGORAG_VERSION=3.0', '', '# 下面别动', 'KEEP=1'].join('\n')
  writeFileSync(p, original)

  setEnvVar(p, 'CONGORAG_VERSION', '4.0')

  // 【就地改的理由就在这里】用户会在 .env 里写注释和笔记，重写整个文件会把
  // 它们抹掉——那是一次升级里最不该发生的事。
  assert.equal(readEnvFile(p).CONGORAG_VERSION, '4.0')
  assert.equal(readEnvFile(p).KEEP, '1')
  const text = readFileSync(p, 'utf8')
  assert.ok(text.includes('# 用户自己的笔记'), '注释必须留着')
  assert.ok(text.includes('# 下面别动'), '注释必须留着')
  assert.equal(text.split('\n').length, original.split('\n').length, '不该多出或少掉行')
})

test('setEnvVar：键不存在就追加到末尾，原内容保留', () => {
  const p = join(scratch(), '.env')
  writeFileSync(p, 'A=1\n')
  setEnvVar(p, 'CONGORAG_VERSION', '3.0')
  assert.deepEqual(readEnvFile(p), { A: '1', CONGORAG_VERSION: '3.0' })
})

test('setEnvVar：注释里的同名键不算数；前缀相同的键不能被误伤', () => {
  const p = join(scratch(), '.env')
  // 注释行里的 CONGORAG_VERSION 不该被改；CONGORAG_VERSION_OLD 是另一个键，
  // 正则少写一个边界就会把它的值改成 "CONGORAG_VERSION=4.0"。
  writeFileSync(p, '# CONGORAG_VERSION=1.0 忘了改\nCONGORAG_VERSION_OLD=2.0\nCONGORAG_VERSION=3.0\n')

  setEnvVar(p, 'CONGORAG_VERSION', '4.0')

  const env = readEnvFile(p)
  assert.equal(env.CONGORAG_VERSION, '4.0')
  assert.equal(env.CONGORAG_VERSION_OLD, '2.0', '前缀相同的另一个键不能被改掉')
  assert.ok(readFileSync(p, 'utf8').includes('# CONGORAG_VERSION=1.0 忘了改'), '注释行不能被当成键')
})

test('setEnvVar：带空格与两侧留白的键也能对上', () => {
  const p = join(scratch(), '.env')
  writeFileSync(p, 'CONGORAG_VERSION   =   3.0\n')
  setEnvVar(p, 'CONGORAG_VERSION', '4.0')
  assert.equal(readEnvFile(p).CONGORAG_VERSION, '4.0')
  assert.doesNotMatch(readFileSync(p, 'utf8'), /3\.0/, '旧值必须被替换掉，不是追加一行')
})

test('setEnvVar：值是字面量，$ 和反斜杠不做替换展开', () => {
  // 用字符串拼接而不是 replace() 的原因：值里的 $& / $1 / \ 在 replace 的
  // 替换串里有特殊含义，会被悄悄改写。
  const p = join(scratch(), '.env')
  writeFileSync(p, 'K=old\n')
  setEnvVar(p, 'K', 'a$&b\\1c$1')
  assert.equal(readEnvFile(p).K, 'a$&b\\1c$1')
})

// ── 卷名解析 ────────────────────────────────────────────────────

test('parseVolumeName：容器不在 / 没匹配到挂载 / 只有空白，都归成 null', () => {
  // 三种"空"必须得出同一个结论。区分它们没有意义——拿到 null 的上层要做的
  // 都是同一件事：报错说读不到卷名，回滚/升级不能继续。
  assert.equal(parseVolumeName(null), null, 'tryRun 失败给的是 null')
  assert.equal(parseVolumeName(undefined), null)
  assert.equal(parseVolumeName(''), null)
  assert.equal(parseVolumeName('\n  \n'), null, '模板没有匹配的挂载时输出空白')
})

test('parseVolumeName：去掉两侧空白后返回真实卷名', () => {
  // compose 的卷名默认是 `<项目名>_<卷名>`，项目名会随目录名 / name: / -p 变，
  // 所以这里只认 docker inspect 读到的那个字符串，不做任何猜测。
  assert.equal(parseVolumeName('congorag_data\n'), 'congorag_data')
  assert.equal(parseVolumeName('  myproj_data  '), 'myproj_data')
})

// ── 备份目录挑选 ────────────────────────────────────────────────

/** 造一份备份目录。raw 给定时直接写进 manifest.json（用来造坏清单）。 */
function makeBackup(root, name, { createdAt, raw } = {}) {
  const dir = join(root, name)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'manifest.json'), raw ?? JSON.stringify({ createdAt }))
  return dir
}

test('pickNewestBackup：根目录不存在或没有备份时返回空，不抛', () => {
  assert.deepEqual(pickNewestBackup(join(scratch(), '没有这个目录')), { dir: null, corrupt: [] })

  const root = scratch() // 存在但是空的
  assert.deepEqual(pickNewestBackup(root), { dir: null, corrupt: [] })
})

test('pickNewestBackup：没有 manifest.json 的目录不算备份，也不算出错', () => {
  const root = scratch()
  mkdirSync(join(root, '用户自己放的东西'), { recursive: true })
  writeFileSync(join(root, 'notes.txt'), '随手记')
  makeBackup(root, '2026-01-01T00-00-00', { createdAt: '2026-01-01T00:00:00.000Z' })

  const picked = pickNewestBackup(root)
  assert.equal(picked.dir, join(root, '2026-01-01T00-00-00'))
  assert.deepEqual(picked.corrupt, [], '不是备份的目录不该被报成"损坏"')
})

test('pickNewestBackup：按清单里的 createdAt 挑最新，不按目录名', () => {
  const root = scratch()
  // 目录名故意和清单里的时间反着来：名字是 --backup-dir 给什么就是什么，
  // 用户手工挪一次就不可信了，清单里的时间才是"新"的定义。
  makeBackup(root, 'a', { createdAt: '2026-09-01T00:00:00.000Z' })
  const newest = makeBackup(root, 'b', { createdAt: '2026-09-20T00:00:00.000Z' })
  makeBackup(root, 'c', { createdAt: '2026-09-10T00:00:00.000Z' })

  assert.equal(pickNewestBackup(root).dir, newest)
})

test('pickNewestBackup：损坏的清单不抛异常，而是被列进 corrupt', () => {
  // 这是 issue #104 的原始症状：原来的写法在 sort 的比较器里 JSON.parse，
  // 目录下任何一份清单坏掉，回滚都会在"选备份"这一步以解析异常崩掉——
  // 看不出是哪个文件，也不知道还能怎么办。
  const root = scratch()
  const broken = makeBackup(root, 'broken', { raw: '{"createdAt": "2026-09' }) // 写了一半的文件
  const ok = makeBackup(root, 'fine', { createdAt: '2026-09-01T00:00:00.000Z' })

  let picked
  assert.doesNotThrow(() => {
    picked = pickNewestBackup(root)
  }, '损坏的清单必须变成一条可读的报错，不能是未捕获的解析异常')

  assert.deepEqual(
    picked.corrupt.map((c) => c.path),
    [broken],
    '损坏的目录要能被点名',
  )
  assert.match(picked.corrupt[0].reason, /\S/, '要带上原始错误文本，不能是空字符串')
  assert.equal(picked.dir, ok, '同一批里还有好的备份时，仍然要挑出它')
})

test('pickNewestBackup：缺 createdAt 的清单也算损坏（排序靠它）', () => {
  // undefined 会让比较器抛 "Cannot read properties of undefined"——
  // 和解析异常一样是莫名其妙的崩法，换成一条能读懂的理由。
  const root = scratch()
  const bad = makeBackup(root, 'no-date', { raw: '{"fromVersion":"3.0"}' })
  const picked = pickNewestBackup(root)
  assert.deepEqual(picked.corrupt.map((c) => c.path), [bad])
  assert.match(picked.corrupt[0].reason, /createdAt/)
  assert.equal(picked.dir, null)
})

test('pickNewestBackup：全是坏的时候 dir 为 null，且一份不漏地列出来', () => {
  const root = scratch()
  const a = makeBackup(root, 'a', { raw: '不是 JSON' })
  const b = makeBackup(root, 'b', { raw: '{"createdAt": 42}' })

  const picked = pickNewestBackup(root)
  assert.equal(picked.dir, null)
  assert.deepEqual(
    picked.corrupt.map((c) => c.path).sort(),
    [a, b].sort(),
  )
})

// ── 哈希 ────────────────────────────────────────────────────────

test('sha256File：对文件内容算，对着公开常量核', () => {
  const p = join(scratch(), 'f')
  writeFileSync(p, 'hello')
  assert.equal(sha256File(p), SHA256_HELLO)
  // 独立算一遍，防止上面的常量被谁"顺手"改成实现算出来的值
  assert.equal(sha256File(p), createHash('sha256').update('hello').digest('hex'))
})
