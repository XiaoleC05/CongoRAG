// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { Schemas } from '@congorag/api-client'
import { api } from '@congorag/api-client'
import PromptsPage from '@/pages/PromptsPage'

// 网络层换成可控 mock（和 pages/AgentDetailPage.test.tsx 同一做法）：
// 这一页要验证的是"注册表数据怎么被摊开"，不是 HTTP 细节。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn() } }))

const getMock = api.GET as unknown as Mock

/**
 * openapi-fetch 成功返回值的替身。
 *
 * 【为什么 response 只写 ok/status】这一页的成功路径只经 unwrap 的
 * `response.ok` 判断；在 jsdom 环境里构造真的 Response 依赖 Node 的全局
 * fetch 是否被测试环境桥接进来，属于环境细节。用能表达判据的最小对象即可。
 */
const OK_RESPONSE = { ok: true, status: 200 } as unknown as Response
const ERROR_RESPONSE = { ok: false, status: 500 } as unknown as Response

const ok = <T,>(data: T) => Promise.resolve({ data, error: undefined, response: OK_RESPONSE })

/** 两个真实感的 SHA-256（64 位小写十六进制）。 */
const CHAT_HASH_V1 = 'd7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592'
const CHAT_HASH_V2 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'
const WRITING_HASH = 'a7ffc6f8bf1ed76651c14756a061d662f580ff4de43b49fa82d80a4b80f8434a'

/**
 * 一条有两个版本的 Prompt：v2 是当前生效版本。
 *
 * 【版本时间与哈希都写实】这一页几乎全部信息都是元数据，fixture 里随便填
 * 会让"渲染出的是不是同一个字段"变得不可断言（例如哈希拿错版本也看不出）。
 */
const chatPrompt: Schemas['PromptEntry'] = {
  key: 'chat.system',
  title: '对话系统提示词',
  description: '决定回答是否逐句标注引用编号。',
  activeVersion: 2,
  versions: [
    {
      version: 1,
      content: '旧版：只依据检索片段回答，不强制标注引用。',
      contentHash: CHAT_HASH_V1,
      changeReason: '首版：通用 RAG 默认提示词',
      createdAt: '2026-09-01T00:00:00Z',
      activatedAt: '2026-09-01T00:00:00Z',
    },
    {
      version: 2,
      content: '新版：每个事实句末尾必须用 [n] 标注引用。',
      contentHash: CHAT_HASH_V2,
      changeReason: '让引用落地检查有可检查对象',
      createdAt: '2026-09-20T00:00:00Z',
      activatedAt: '2026-09-20T00:00:00Z',
      sampling: { temperature: 0.2, maxTokens: 2048, note: '低温度减少自由发挥' },
    },
  ],
  variables: [
    { name: 'chunks', description: '检索片段（按编号拼接）', required: true },
    { name: 'question', description: '用户问题', required: false },
  ],
  codeRefs: [{ file: 'internal/conversation/usecase.go', symbol: 'defaultSystemPrompt' }],
}

/** 只有一版、没有变量与采样参数的条目：用来验证"没有的东西不渲染"。 */
const writingPrompt: Schemas['PromptEntry'] = {
  key: 'writing.system',
  title: '写作 Copilot 提示词',
  description: '基于检索结果生成带引用的写作草稿。',
  activeVersion: 1,
  versions: [
    {
      version: 1,
      content: '只依据编号资料写作；每个事实句末尾用 [n]。',
      contentHash: WRITING_HASH,
      changeReason: '首版',
      createdAt: '2026-09-01T00:00:00Z',
      activatedAt: '2026-09-01T00:00:00Z',
    },
  ],
  variables: [],
  codeRefs: [{ file: 'apps/api/internal/api/server.go', symbol: 'buildWritingPrompt' }],
}

function mockCatalog(data: Schemas['PromptEntry'][]) {
  getMock.mockImplementation((path: string) => {
    if (path === '/api/v1/prompts') return ok(data)
    // 用例里出现没预料到的路径时直接炸，而不是静默返回 undefined：
    // 静默会让页面停在"加载中"，看起来像用例写得对但页面坏了。
    throw new Error(`用例没预料到的 GET ${path}`)
  })
}

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <PromptsPage />
    </QueryClientProvider>,
  )
}

describe('Prompt 审计页', () => {
  afterEach(cleanup)
  beforeEach(() => {
    getMock.mockReset()
  })

  it('渲染注册表条目：标题、key、生效版本、变量、代码位置与哈希', async () => {
    mockCatalog([chatPrompt, writingPrompt])
    renderPage()

    expect(await screen.findByRole('heading', { level: 1, name: 'Prompt 审计' })).toBeTruthy()
    // 首屏那句"存在理由"是这一页的核心文案，别在改版时悄悄删掉。
    expect(screen.getByText(/提示词不是藏在代码里的字符串/)).toBeTruthy()

    // 每个条目一张卡片、标题是 h2：层级不跳（headingStructure.test.ts 管 h1）。
    // 用 findBy 等数据到齐——h1 在加载态就渲染了，找到它不代表卡片已经在了。
    expect(await screen.findByRole('heading', { level: 2, name: '对话系统提示词' })).toBeTruthy()
    expect(screen.getByRole('heading', { level: 2, name: '写作 Copilot 提示词' })).toBeTruthy()
    expect(screen.getByText('chat.system')).toBeTruthy()
    expect(screen.getByText('Active v2')).toBeTruthy()

    // 变量：必填/可选都要看得见——这是"提示词契约"里运行时的那一半。
    expect(screen.getByText('chunks')).toBeTruthy()
    expect(screen.getByText('必填')).toBeTruthy()
    expect(screen.getByText('question')).toBeTruthy()
    expect(screen.getByText('可选')).toBeTruthy()

    // 代码位置是 file:symbol 一整串：分开显示就看不出"去哪改"。
    expect(
      screen.getByText('internal/conversation/usecase.go:defaultSystemPrompt'),
    ).toBeTruthy()

    // 只显示哈希前 12 位，完整值在 title 上（悬停/读屏可拿到）。
    const hash = screen.getByTitle(CHAT_HASH_V2)
    expect(hash.textContent).toBe(`${CHAT_HASH_V2.slice(0, 12)}…`)

    // 采样参数有值才显示，且 temperature / maxTokens 都在。
    expect(screen.getByText(/temperature 0\.2/)).toBeTruthy()
    expect(screen.getByText(/maxTokens 2048/)).toBeTruthy()
    expect(screen.getAllByText(/temperature/)).toHaveLength(1)

    expect(getMock).toHaveBeenCalledWith('/api/v1/prompts')
  })

  it('切换版本后展示内容随之变化，且不重新请求', async () => {
    mockCatalog([chatPrompt])
    renderPage()

    // 默认展示的是 activeVersion（v2），不是"编号第一"或"最后一条"。
    expect(await screen.findByText(/每个事实句末尾必须用 \[n\] 标注引用/)).toBeTruthy()
    expect(screen.getByTitle(CHAT_HASH_V2)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'v1' }))

    expect(screen.getByText(/旧版：只依据检索片段回答/)).toBeTruthy()
    expect(screen.queryByText(/每个事实句末尾必须用/)).toBeNull()
    // 哈希与变更原因都必须跟着选中版本换：只换正文、元数据还停在 v2 是
    // 最典型的"看起来对了、审的是错版本"。
    expect(screen.getByTitle(CHAT_HASH_V1)).toBeTruthy()
    expect(screen.getByText('首版：通用 RAG 默认提示词')).toBeTruthy()

    // 版本历史一次下发：切换是纯前端状态，请求只发生过一次。
    expect(getMock).toHaveBeenCalledTimes(1)
  })

  it('只有一个版本时不渲染版本切换，也不渲染没有的采样参数', async () => {
    mockCatalog([writingPrompt])
    renderPage()

    await screen.findByRole('heading', { level: 2, name: '写作 Copilot 提示词' })
    expect(
      screen.queryByRole('group', { name: '写作 Copilot 提示词 的版本' }),
    ).toBeNull()
    // "没有采样参数"不是需要展示的信息，整行不渲染。
    expect(screen.queryByText('采样参数')).toBeNull()
    // 没有变量时给一句解释，而不是留一个空区块。
    expect(screen.getByText(/无变量/)).toBeTruthy()
  })

  it('空态：注册表为空时给出排查方向，不是一片空白', async () => {
    mockCatalog([])
    renderPage()

    expect(await screen.findByText('注册表里还没有条目')).toBeTruthy()
    expect(screen.getByText(/检查一下服务端日志/)).toBeTruthy()
  })

  it('错误态：页内 Alert + 按 type 映射的中文，而不是后端 JSON 原文', async () => {
    getMock.mockImplementation(() =>
      Promise.resolve({
        data: undefined,
        error: { type: 'internal_error', title: '内部错误', status: 500 },
        response: ERROR_RESPONSE,
      }),
    )
    renderPage()

    expect(await screen.findByText('加载失败')).toBeTruthy()
    // ErrorText 走 lib/errors.ts 的 MESSAGES：这句话只能来自 type 映射。
    expect(screen.getByText('服务内部错误')).toBeTruthy()
  })
})
