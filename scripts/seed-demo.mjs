#!/usr/bin/env node
/**
 * 演示数据种子脚本（评审/复现用）。
 *
 * 用法：
 *   node scripts/seed-demo.mjs --api http://127.0.0.1:3210 --dir ./测试数据/pdf
 *   node scripts/seed-demo.mjs --api ... --dir ... --dry-run
 *
 * 做什么：
 *   1. 建 3 个演示知识库（PDF 学术解析 / 版本去重 / 引用图谱）；
 *   2. 上传目录里的 PDF（Attention v1/v7/scanned、survey、UDL 等，文件名按需匹配）；
 *   3. 等待 ready；对版本去重库的重复告警执行 keep_both；
 *   4. 建一个 arXiv Agent；
 *   5. 打印后续可用 ID，供评审访问。
 *
 * 【为什么用脚本而不是 dump 数据库】数据库 dump 绑定了本机 schema 与模型配置；
 * 脚本只走公开 API，任何按 README 起好的实例都能复现，也不包含 API Key。
 */
import fs from 'node:fs'
import path from 'node:path'

const args = process.argv.slice(2)
const opt = (name, fallback = '') => {
  const i = args.indexOf('--' + name)
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback
}
const api = opt('api', 'http://127.0.0.1:3210').replace(/\/$/, '')
const dir = opt('dir', './测试数据/pdf')
const dryRun = args.includes('--dry-run')

const FILES = {
  v1: /1706\.03762v1\.pdf$/i,
  v7: /1706\.03762v7\.pdf$/i,
  scan: /1706\.03762v1_img\.pdf$/i,
  survey: /survey.*\.pdf$/i,
  udl: /UnderstandingDeepLearning.*\.pdf$/i,
}

async function json(pathname, method = 'GET', body) {
  const res = await fetch(api + pathname, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  if (!res.ok) throw new Error(pathname + ' ' + res.status + ' ' + text.slice(0, 200))
  return text ? JSON.parse(text) : null
}

async function upload(kbId, file) {
  const fd = new FormData()
  fd.append('file', new Blob([fs.readFileSync(file)]), path.basename(file))
  const res = await fetch(api + '/api/v1/knowledge-bases/' + kbId + '/documents', {
    method: 'POST',
    body: fd,
  })
  const text = await res.text()
  if (!res.ok) throw new Error('upload ' + path.basename(file) + ' ' + res.status + ' ' + text.slice(0, 200))
  return JSON.parse(text)
}

async function waitReady(docId, maxSec = 900) {
  const start = Date.now()
  while (Date.now() - start < maxSec * 1000) {
    await new Promise((r) => setTimeout(r, 3000))
    const doc = await json('/api/v1/documents/' + docId)
    if (doc.status === 'ready') return doc
    if (doc.status === 'failed') throw new Error('处理失败：' + doc.filename)
  }
  throw new Error('等待超时：' + docId)
}

function pickFiles() {
  if (!fs.existsSync(dir)) throw new Error('目录不存在：' + dir)
  const found = {}
  for (const name of fs.readdirSync(dir)) {
    for (const [key, re] of Object.entries(FILES)) {
      if (!found[key] && re.test(name)) found[key] = path.join(dir, name)
    }
  }
  return found
}

async function main() {
  const files = pickFiles()
  console.log('API：' + api)
  console.log('文件：', Object.keys(files).join(', ') || '(无)')
  if (dryRun) {
    console.log('dry-run：不会真正创建数据。')
    return
  }
  const ids = {}

  await json('/api/v1/knowledge-bases', 'POST', { name: 'PDF 学术解析验证' }).then(async (kb) => {
    ids.kbPdf = kb.id
    for (const key of ['v1', 'v7', 'scan', 'survey']) {
      if (!files[key]) continue
      const up = await upload(kb.id, files[key])
      const doc = await waitReady(up.id)
      console.log('ready', doc.filename, doc.chunkCount, '块')
    }
  })

  await json('/api/v1/knowledge-bases', 'POST', { name: 'UDL 长文档验证' }).then(async (kb) => {
    ids.kbUdl = kb.id
    if (files.udl) {
      const up = await upload(kb.id, files.udl)
      const doc = await waitReady(up.id, 1800)
      console.log('ready', doc.filename, doc.chunkCount, '块')
    }
  })

  await json('/api/v1/knowledge-bases', 'POST', { name: '版本去重演示（v7 → v1）' }).then(async (kb) => {
    ids.kbDedupe = kb.id
    for (const key of ['v7', 'v1']) {
      if (!files[key]) continue
      const up = await upload(kb.id, files[key])
      await waitReady(up.id)
    }
    const docs = await json('/api/v1/knowledge-bases/' + kb.id + '/documents')
    for (const d of docs.items) {
      if (d.duplicateOf) {
        await fetch(api + '/api/v1/documents/' + d.id + '/duplicate-resolution', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ action: 'keep_both' }),
        })
        console.log('保留两者：', d.filename)
      }
    }
  })

  await json('/api/v1/knowledge-bases', 'POST', { name: '引用图谱演示（survey → Attention）' }).then(async (kb) => {
    ids.kbGraph = kb.id
    for (const key of ['survey', 'v7', 'v1']) {
      if (!files[key]) continue
      const up = await upload(kb.id, files[key])
      await waitReady(up.id)
    }
  })

  const agent = await json('/api/v1/agents', 'POST', {
    name: 'arXiv 文献助手',
    description: '检索 arXiv 论文',
    instruction: '用 arxiv_search 工具检索论文，返回标题/作者/年份/链接与一句话要点。',
    toolNames: ['arxiv_search'],
  })
  ids.agent = agent.id
  console.log('完成。知识库/Agent ID：', JSON.stringify(ids, null, 2))
}

main().catch((e) => {
  console.error('种子脚本失败：', e.message)
  process.exit(1)
})
