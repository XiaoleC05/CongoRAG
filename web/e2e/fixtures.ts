import { expect, test as base } from '@playwright/test'
import type { ConsoleMessage, Page } from '@playwright/test'

/**
 * E2E 的公共底座：契约形状的 fixture + 全量 API mock + 错误收集。
 *
 * 【数据形状从哪来】全部照 `packages/api-client/src/schema.d.ts`（也就是
 * contracts/openapi.yaml）抄，字段一个不少。这一点是刻意的：mock 数据如果
 * 只有"页面用到的那几个字段"，某天组件开始读一个新字段时 E2E 会静默显示
 * undefined，而在真实后端上却是好的——那样的 E2E 会给人虚假的安全感。
 *
 * 【外部请求一律 abort】页面里没有外部字体/图片，但 `@fontsource` 与
 * 开发服务器的 preload 之外，任何第三个域名的请求都说明有东西偷偷加了
 * 外链。abort 之后它会作为 console error 出现在用例里（见 consoleErrors），
 * 这正是我们要的：E2E 不允许依赖公网。
 */

export const IDS = {
  provider: '11111111-1111-4111-8111-111111111111',
  model: '22222222-2222-4222-8222-222222222222',
  kb: '33333333-3333-4333-8333-333333333333',
  conversation: '44444444-4444-4444-8444-444444444444',
  agent: '55555555-5555-4555-8555-555555555555',
  run: '66666666-6666-4666-8666-666666666666',
  doc: '77777777-7777-4777-8777-777777777777',
} as const

const MODEL = {
  id: IDS.model,
  modelId: 'test-chat-model',
  kind: 'chat',
  capabilities: { chat: true, streaming: true, toolCalling: true, reasoning: false },
  contextWindow: 32000,
  maxOutputTokens: 4096,
  tokenizerType: 'cl100k_base',
  embeddingDim: 0,
  createdAt: '2026-09-01T00:00:00Z',
}

const PROVIDER = {
  id: IDS.provider,
  baseUrl: 'https://api.example.com/v1',
  createdAt: '2026-09-01T00:00:00Z',
  models: [MODEL],
  requeuedDocuments: null,
}

const KB = {
  id: IDS.kb,
  name: 'Transformer 论文库',
  createdAt: '2026-09-01T00:00:00Z',
  updatedAt: '2026-09-01T00:00:00Z',
}

const CONVERSATION = {
  id: IDS.conversation,
  title: '关于注意力机制的讨论',
  knowledgeBaseId: IDS.kb,
  createdAt: '2026-09-02T00:00:00Z',
  updatedAt: '2026-09-02T00:00:00Z',
}

const MESSAGE = {
  id: '88888888-8888-4888-8888-888888888888',
  conversationId: IDS.conversation,
  role: 'assistant',
  content: '注意力机制让模型在长序列上按相关性取信息。',
  status: 'completed',
  sequenceNo: 1,
  createdAt: '2026-09-02T00:00:01Z',
  citations: [],
  grounding: null,
}

const AGENT = {
  id: IDS.agent,
  name: '算数助手',
  description: '会调用 calculator 工具的 Agent',
  instruction: '你是一个算数助手。',
  toolNames: ['calculator'],
  createdAt: '2026-09-03T00:00:00Z',
  updatedAt: '2026-09-03T00:00:00Z',
}

const AGENT_RUN = {
  id: IDS.run,
  agentId: IDS.agent,
  status: 'completed',
  currentStep: 2,
  input: '算一下 1+1',
  output: '2',
  createdAt: '2026-09-03T00:00:01Z',
  updatedAt: '2026-09-03T00:00:02Z',
}

const RUN_STEP = {
  id: '99999999-9999-4999-8999-999999999999',
  runId: IDS.run,
  seq: 1,
  type: 'tool',
  status: 'completed',
  toolName: 'calculator',
  toolArgs: { expression: '1+1' },
  toolResult: { value: 2 },
  latencyMs: 3,
  error: null,
  createdAt: '2026-09-03T00:00:01Z',
}

const USAGE = {
  totalPromptTokens: 3263,
  totalCompletionTokens: 225,
  byModel: [
    {
      providerId: IDS.provider,
      modelId: IDS.model,
      modelName: 'test-chat-model',
      kind: 'chat',
      calls: 5,
      promptTokens: 3263,
      completionTokens: 225,
    },
  ],
}

const PROMPTS = [
  {
    key: 'chat.system',
    title: '对话系统提示词',
    description: '决定回答是否逐句标注引用编号。',
    activeVersion: 2,
    versions: [
      {
        version: 1,
        content: '旧版：只依据检索片段回答。',
        contentHash: 'd7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592',
        changeReason: '首版',
        createdAt: '2026-09-01T00:00:00Z',
        activatedAt: '2026-09-01T00:00:00Z',
      },
      {
        version: 2,
        content: '新版：每个事实句末尾必须用 [n] 标注引用。',
        contentHash: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
        changeReason: '让引用落地检查有可检查对象',
        createdAt: '2026-09-20T00:00:00Z',
        activatedAt: '2026-09-20T00:00:00Z',
        sampling: { temperature: 0.2, maxTokens: 2048, note: '低温度减少自由发挥' },
      },
    ],
    variables: [{ name: 'chunks', description: '检索片段', required: true }],
    codeRefs: [{ file: 'internal/conversation/usecase.go', symbol: 'defaultSystemPrompt' }],
  },
  {
    key: 'writing.system',
    title: '写作 Copilot 提示词',
    description: '基于检索结果生成带引用的写作草稿。',
    activeVersion: 1,
    versions: [
      {
        version: 1,
        content: '只依据编号资料写作；每个事实句末尾用 [n]。',
        contentHash: 'a7ffc6f8bf1ed76651c14756a061d662f580ff4de43b49fa82d80a4b80f8434a',
        changeReason: '首版',
        createdAt: '2026-09-01T00:00:00Z',
        activatedAt: '2026-09-01T00:00:00Z',
      },
    ],
    variables: [],
    codeRefs: [],
  },
]

const DOCUMENT = {
  id: IDS.doc,
  knowledgeBaseId: IDS.kb,
  filename: 'sample.pdf',
  status: 'ready',
  byteSize: 1024,
  chunkCount: 3,
  pageCount: 1,
  parser: 'pdf-service-0.1.0',
  createdAt: '2026-09-05T00:00:00Z',
  updatedAt: '2026-09-05T00:00:00Z',
}

const OUTLINE = [{ title: '1 Introduction', page: 1, level: 1 }]
const REFERENCES = [
  {
    index: 1,
    text: 'Vaswani et al. Attention Is All You Need. 2017.',
    page: 1,
    matchedDocumentId: null,
    matchedFilename: null,
  },
]

/**
 * 一份**最小但合法**的 PDF（1 页，一行业务文本）。
 *
 * 【为什么在代码里生成而不是提交一个二进制】fixture 只要能被 pdf.js 解析、
 * 能渲染出一页，就足够验证阅读器的工具栏/窄屏抽屉/无溢出；真实论文的
 * 3MB 二进制进仓库没有意义。xref 的字节偏移必须逐项算对，所以这里用
 * "按顺序写、记录偏移"的方式生成，而不是手写一段常量字符串。
 */
export function buildMinimalPdf(): Buffer {
  const chunks: Buffer[] = []
  let offset = 0
  const write = (text: string) => {
    const buf = Buffer.from(text, 'latin1')
    chunks.push(buf)
    offset += buf.length
  }
  const offsets: number[] = []

  write('%PDF-1.4\n')

  offsets[1] = offset
  write('1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n')

  offsets[2] = offset
  write('2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n')

  offsets[3] = offset
  write(
    '3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] ' +
      '/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n',
  )

  const content = 'BT /F1 24 Tf 72 760 Td (CongoRAG E2E sample) Tj ET\n'
  offsets[4] = offset
  write(`4 0 obj\n<< /Length ${Buffer.byteLength(content, 'latin1')} >>\nstream\n${content}endstream\nendobj\n`)

  offsets[5] = offset
  write('5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n')

  const xrefOffset = offset
  let xref = 'xref\n0 6\n0000000000 65535 f \n'
  for (let i = 1; i <= 5; i += 1) {
    xref += `${String(offsets[i]).padStart(10, '0')} 00000 n \n`
  }
  write(xref)
  write(`trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n${xrefOffset}\n%%EOF\n`)

  return Buffer.concat(chunks)
}

const PDF = buildMinimalPdf()

export type MockApiResult = {
  /** 没有被 mock 命中的 API（"METHOD /path"），用例结束时断言为空。 */
  unmatched: string[]
}

/**
 * 把整个 /api/v1/** 接管掉，并 abort 所有非本机请求。
 *
 * 【返回值里的 unmatched 很重要】漏 mock 的接口默认返回 404 Problem，
 * 页面会显示错误态——但"错误态也有 h1"，用例可能照样绿。所以把未命中的
 * key 收集起来、由用例显式断言为空：新页面加了接口而 fixture 没跟上时，
 * 失败信息会直接给出那个 URL。
 */
export async function mockApi(page: Page): Promise<MockApiResult> {
  const unmatched: string[] = []

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const pathname = new URL(request.url()).pathname
    const key = `${request.method()} ${pathname}`

    switch (key) {
      case 'GET /api/v1/providers':
        return route.fulfill({ json: [PROVIDER] })
      case 'GET /api/v1/knowledge-bases':
        return route.fulfill({ json: [KB] })
      case 'GET /api/v1/conversations':
        return route.fulfill({ json: { items: [CONVERSATION], nextCursor: null } })
      case `GET /api/v1/conversations/${IDS.conversation}/messages`:
        return route.fulfill({ json: { items: [MESSAGE], nextCursor: null } })
      case 'GET /api/v1/agents':
        return route.fulfill({ json: [AGENT] })
      case `GET /api/v1/agents/${IDS.agent}`:
        return route.fulfill({ json: AGENT })
      case `GET /api/v1/agents/${IDS.agent}/runs`:
        return route.fulfill({ json: { items: [AGENT_RUN], nextCursor: null } })
      case `GET /api/v1/runs/${IDS.run}/steps`:
        return route.fulfill({ json: [RUN_STEP] })
      case 'GET /api/v1/tools':
        return route.fulfill({ json: [] })
      case 'GET /api/v1/usage':
        return route.fulfill({ json: USAGE })
      case 'GET /api/v1/prompts':
        return route.fulfill({ json: PROMPTS })
      case `GET /api/v1/documents/${IDS.doc}`:
        return route.fulfill({ json: DOCUMENT })
      case `GET /api/v1/documents/${IDS.doc}/outline`:
        return route.fulfill({ json: OUTLINE })
      case `GET /api/v1/documents/${IDS.doc}/references`:
        return route.fulfill({ json: REFERENCES })
      case `GET /api/v1/documents/${IDS.doc}/file`:
        return route.fulfill({ status: 200, contentType: 'application/pdf', body: PDF })
      case 'POST /api/v1/translate':
        return route.fulfill({ json: { translation: '（mock）这是 E2E 的译文。' } })
      default: {
        unmatched.push(key)
        return route.fulfill({
          status: 404,
          contentType: 'application/problem+json',
          body: JSON.stringify({ type: 'not_found', title: 'E2E 没有 mock 这个接口', status: 404 }),
        })
      }
    }
  })

  // 所有非本机请求一律 abort：E2E 不依赖公网。失败时它会以 console error
  // 的形式被 consoleErrors 收集到，用例的失败信息里能直接看到是哪个 URL。
  await page.route(/^https?:\/\/(?!localhost|127\.0\.0\.1)/, (route) => route.abort())

  return { unmatched }
}

/** 等页面"真的加载完"：Suspense 兜底骨架与页面内部骨架都消失。 */
export async function expectLoaded(page: Page): Promise<void> {
  await expect(page.locator('[data-slot="skeleton"]')).toHaveCount(0, { timeout: 20_000 })
}

/**
 * 断言没有水平溢出。
 *
 * 【为什么是 scrollWidth - clientWidth 而不是看 scrollWidth > innerWidth】
 * 桌面端可能出竖向滚动条（clientWidth 已扣掉它）；用 innerWidth 比会在
 * 差 15px 的时候误报。阈值留 1px 给亚像素舍入。
 */
export async function expectNoHorizontalOverflow(page: Page, label: string): Promise<void> {
  const overflow = await page.evaluate(() => {
    const el = document.documentElement
    return el.scrollWidth - el.clientWidth
  })
  expect(overflow, `${label}：出现 ${overflow}px 横向溢出`).toBeLessThanOrEqual(1)
}

/** 把 console.error / pageerror 收进数组，用例末尾断言为空。 */
type Fixtures = {
  consoleErrors: string[]
}

export const test = base.extend<Fixtures>({
  consoleErrors: [
    async ({ page }, use) => {
      const errors: string[] = []
      page.on('console', (message: ConsoleMessage) => {
        if (message.type() === 'error') errors.push(message.text())
      })
      page.on('pageerror', (error) => errors.push(`pageerror: ${error.message}`))
      await use(errors)
    },
    { auto: true },
  ],
})

export { expect }
