#!/usr/bin/env node
/**
 * 检索/引用评测脚本（参赛版 M2/M5 的可复现证据）。
 *
 * 【它测什么】
 *   1. 检索命中：给定问题与"期望命中的文档+页码"，调检索调试接口，
 *      统计 hit@1 / hit@3 / MRR；
 *   2. 引用页码：走真实问答链路，检查回答的 citation 里是否包含期望页码；
 *   3. 引用落地：读取 grounding 事件/持久化结果，统计 valid 比例。
 *
 * 【怎么跑】
 *   1. 先启动服务（Docker 或 make dev），配好模型并上传演示文档；
 *   2. 复制 config.example.json 为 config.json，把 knowledgeBaseId / 文档 id
 *      换成你自己的库（本脚本不内置任何真实 id）；
 *   3. node retrieval-eval.mjs config.json
 *
 * 【为什么把期望页码写成 any-of】同一处内容在 PDF 的不同版本/OCR 里可能落在
 * 相邻两页（表格跨页、阅读顺序差异），要求"精确等于某一页"会把正确召回判错；
 * 评测集也刻意保持小规模——它证明的是"这条链路可量化"，不是刷高分。
 */
import fs from 'node:fs'

const cfgPath = process.argv[2] || 'config.json'
const cfg = JSON.parse(fs.readFileSync(cfgPath, 'utf8'))
const BASE = cfg.baseUrl || 'http://127.0.0.1:3210/api/v1'
const docsById = cfg.documents // { name: uuid }

async function search(kbId, query, topK = 5) {
  const res = await fetch(BASE + '/knowledge-bases/' + kbId + '/search', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query, topK }),
  })
  if (!res.ok) throw new Error('search failed: ' + res.status + ' ' + (await res.text()))
  return res.json()
}

async function ask(kbId, text) {
  const conv = await (
    await fetch(BASE + '/conversations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: 'eval', knowledgeBaseId: kbId }),
    })
  ).json()
  const resp = await fetch(BASE + '/conversations/' + conv.id + '/messages', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  })
  await resp.text() // 消费完整 SSE，确保本轮结束
  const msgs = await (await fetch(BASE + '/conversations/' + conv.id + '/messages')).json()
  return msgs.items.filter((m) => m.role === 'assistant').pop()
}

async function main() {
  const retrieval = { rows: [], hitAt1: 0, hitAt3: 0, mrr: 0 }
  for (const c of cfg.cases) {
    const out = await search(c.kb, c.query)
    let rank = 0
    for (let i = 0; i < (out.hits || []).length; i++) {
      const h = out.hits[i]
      const ok = c.want.some(
        (w) => docsById[w.doc] === h.documentId && (w.pages.includes(0) || w.pages.includes(h.page)),
      )
      if (ok) { rank = i + 1; break }
    }
    if (rank === 1) retrieval.hitAt1++
    if (rank > 0 && rank <= 3) retrieval.hitAt3++
    if (rank > 0) retrieval.mrr += 1 / rank
    retrieval.rows.push({ query: c.query, rank, top: (out.hits || []).slice(0, 3).map((h) => h.page) })
  }
  const n = cfg.cases.length
  retrieval.hitAt1 = +(retrieval.hitAt1 / n).toFixed(3)
  retrieval.hitAt3 = +(retrieval.hitAt3 / n).toFixed(3)
  retrieval.mrr = +(retrieval.mrr / n).toFixed(3)

  const citation = { rows: [] }
  for (const c of cfg.citationCases) {
    const a = await ask(c.kb, c.query)
    const pages = (a.citations || []).map((x) => x.page).filter((p) => p != null)
    citation.rows.push({
      query: c.query,
      citationPages: pages,
      expectedPages: c.pages,
      pageHit: pages.some((p) => c.pages.includes(p)),
      groundingValid: a.grounding ? a.grounding.valid : null,
    })
  }

  const report = { retrieval, citation }
  console.log(JSON.stringify(report, null, 1))
  fs.writeFileSync('eval-results.json', JSON.stringify(report, null, 1))
}

main().catch((e) => { console.error(e); process.exit(1) })
