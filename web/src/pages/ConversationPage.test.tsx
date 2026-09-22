// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { Schemas } from '@congorag/api-client'
import { api } from '@congorag/api-client'
import ConversationPage from '@/pages/ConversationPage'
import { streamChat } from '@/lib/streamChat'

type Message = Schemas['Message']

// 网络层换成可控 mock：这里要验证的是"点了停止之后界面进哪个状态""重试 /
// 重新生成把什么发给了服务端"，真实网络只会让这两件事更难摆出来。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))
vi.mock('@/lib/streamChat', () => ({ streamChat: vi.fn() }))

const getMock = api.GET as unknown as Mock

// jsdom 不实现 scrollIntoView，而对话页在"来了新消息 / 流式追加"时会调它
// （滚到底部）。补在这里而不是 src/test/setup.ts：那个文件是所有测试文件
// 共用的，而只有渲染整页、且页面里有滚动容器的用例才会碰到它。
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
})

const CID = 'c-1'
const MESSAGES_PATH = '/api/v1/conversations/{id}/messages'

const question: Message = {
  id: '11111111-1111-4111-8111-111111111111',
  conversationId: CID,
  role: 'user',
  content: '向量检索用什么索引？',
  status: 'completed',
  sequenceNo: 1,
  createdAt: '2026-09-22T00:00:00Z',
}

const answer: Message = {
  id: '22222222-2222-4222-8222-222222222222',
  conversationId: CID,
  role: 'assistant',
  content: 'HNSW。',
  status: 'completed',
  sequenceNo: 2,
  createdAt: '2026-09-22T00:00:01Z',
}

/** 被中止 / 失败的那一轮：后端把已经生成的部分写进 content，状态是 failed。 */
const failedAnswer: Message = {
  id: '33333333-3333-4333-8333-333333333333',
  conversationId: CID,
  role: 'assistant',
  content: 'HNS',
  status: 'failed',
  sequenceNo: 3,
  createdAt: '2026-09-22T00:00:02Z',
}

/** 刚被中止的那一轮：它的 id 必须是这次流新写出来的那一行。 */
const abortedAnswer: Message = {
  ...failedAnswer,
  id: '44444444-4444-4444-8444-444444444444',
}

function mockHistory(initial: Message[]) {
  let persisted = initial
  getMock.mockImplementation((path: string) => {
    if (path !== MESSAGES_PATH) throw new Error(`用例没预料到的 GET ${path}`)
    return Promise.resolve({ data: { items: persisted, nextCursor: null }, error: undefined })
  })
  return {
    /** 模拟后端在某个时刻把新的一行落库（中止那一刻、重试那一刻……）。 */
    set: (next: Message[]) => {
      persisted = next
    },
  }
}

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/conversations/${CID}`]}>
        <Routes>
          <Route path="/conversations/:id" element={<ConversationPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function type(text: string) {
  fireEvent.change(screen.getByPlaceholderText('输入消息…'), { target: { value: text } })
}

afterEach(() => {
  getMock.mockReset()
  vi.mocked(streamChat).mockReset()
  cleanup()
})

describe('停止生成（issue #79）', () => {
  it('流式进行中发送换成停止；按下之后是「已停止生成」，不是「生成失败」', async () => {
    let signal: AbortSignal | undefined
    const history = mockHistory([question])

    vi.mocked(streamChat).mockImplementation((_id, _text, _key, callbacks, abort) => {
      signal = abort
      callbacks.onEvent({ type: 'token', id: 1, data: { text: 'HN' } })
      return new Promise<number | null>((resolve) => {
        abort?.addEventListener('abort', () => {
          // 真后端在中止那一刻做的事：把已经生成的部分以 failed 落库
          //（conversation.failMessage），用户的消息本来就已经在里面了。
          history.set([question, abortedAnswer])
          resolve(1)
        })
      })
    })

    renderPage()
    await screen.findByText(question.content)

    type('再问一次')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))

    // 发送按钮在同一位置换成了停止按钮
    const stop = await screen.findByRole('button', { name: '停止生成' })
    expect(screen.queryByRole('button', { name: '发送' })).toBeNull()
    fireEvent.click(stop)

    // 中止真的落到了这条流上（不是只改了个 state）
    await waitFor(() => expect(signal?.aborted).toBe(true))

    // 【中止态不是 error 态】页面上没有"生成失败"，而是一条会留下来的"已停止"
    expect(await screen.findByText('已停止生成')).toBeTruthy()
    expect(screen.queryByText('生成失败')).toBeNull()
    // 提交按钮回来了（可以继续打字）
    expect(await screen.findByRole('button', { name: '发送' })).toBeTruthy()
  })

  it('停止之后那条回答有「重试」入口，重试带的是同一句提问', async () => {
    const history = mockHistory([question])
    let signal: AbortSignal | undefined

    vi.mocked(streamChat).mockImplementation((_id, _text, _key, _callbacks, abort) => {
      signal = abort
      return new Promise<number | null>((resolve) => {
        abort?.addEventListener('abort', () => {
          history.set([question, failedAnswer])
          resolve(1)
        })
      })
    })

    renderPage()
    await screen.findByText(question.content)
    type('随便问')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    fireEvent.click(await screen.findByRole('button', { name: '停止生成' }))
    await waitFor(() => expect(signal?.aborted).toBe(true))

    // 重试入口和"已停止生成"在同一行——用户按下停止之后最可能的下一步
    const retry = await screen.findByRole('button', { name: '重试' })
    expect(screen.getByText('已停止生成')).toBeTruthy()

    // 重试要能真的再发一次：第二段流（这次让它正常结束）
    vi.mocked(streamChat).mockResolvedValue(null)
    fireEvent.click(retry)

    await waitFor(() => expect(vi.mocked(streamChat)).toHaveBeenCalledTimes(2))
    const [, text] = vi.mocked(streamChat).mock.calls[1]
    expect(text).toBe(question.content)
  })
})

describe('重新生成（issue #90）', () => {
  it('成功的回答有入口；旧回答保留为一条分支并在界面上看得出来', async () => {
    mockHistory([question, answer])
    vi.mocked(streamChat).mockResolvedValue(null)

    renderPage()
    await screen.findByText(answer.content)

    // 失败的那一轮才给「重试」，成功的回答给的是「重新生成」——两个入口
    // 不是同一件事，不能同时出现。
    expect(screen.getByRole('button', { name: '重新生成' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: '重新生成' }))

    await waitFor(() => expect(vi.mocked(streamChat)).toHaveBeenCalledTimes(1))
    const [id, text] = vi.mocked(streamChat).mock.calls[0]
    expect(id).toBe(CID)
    // 同一句提问再来一次（不是"再发一条新消息"）
    expect(text).toBe(question.content)

    // 【旧回答不删、不当没发生】后端没有删消息的端点，那一轮已经落库、
    // 已经计过费；前端把它藏起来只会让界面和数据库分叉。标出来让分支看得见。
    expect(await screen.findByText('已被重新生成')).toBeTruthy()
    expect(screen.getByText(answer.content)).toBeTruthy()
  })

  it('重新生成用的是新的幂等键——不是同一次尝试的重放', async () => {
    mockHistory([question, answer])
    vi.mocked(streamChat).mockResolvedValue(null)

    renderPage()
    await screen.findByText(answer.content)

    // 先正常发一条，记下它的键
    type('新问题')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(vi.mocked(streamChat)).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: '停止生成' })).toBeNull(),
    )

    fireEvent.click(screen.getByRole('button', { name: '重新生成' }))
    await waitFor(() => expect(vi.mocked(streamChat)).toHaveBeenCalledTimes(2))

    const [sendKey, regenKey] = vi.mocked(streamChat).mock.calls.map((c) => c[2])
    expect(sendKey).not.toBe(regenKey)
    for (const key of [sendKey, regenKey]) {
      expect(key.length).toBeGreaterThan(0)
    }
  })

  // 失败的那一轮：入口是「重试」，而且点它要真的重新调一次模型（新键）——
  // 复用同一个键是"把上一轮的事件补发一遍"，而上一轮记下来的终态就是那条
  // error 帧，用户会立刻再看到同一个错误。
  it('失败的那一轮给的是「重试」，且不是同键重放', async () => {
    mockHistory([question, failedAnswer])
    vi.mocked(streamChat).mockResolvedValue(null)

    renderPage()
    await screen.findByText(failedAnswer.content)

    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))

    await waitFor(() => expect(vi.mocked(streamChat)).toHaveBeenCalledTimes(1))
    const [, text, key] = vi.mocked(streamChat).mock.calls[0]
    expect(text).toBe(question.content)
    expect(key.length).toBeGreaterThan(0)
  })

  // README §16：返回 Promise 的操作要看得见"正在做"，并且不能重复提交。
  it('重试进行中：那一行显示「重试中…」，按钮不再可点', async () => {
    mockHistory([question, failedAnswer])
    // 这次让它挂着不结束——那就是"正在重试"。
    vi.mocked(streamChat).mockImplementation(() => new Promise<number | null>(() => {}))

    renderPage()
    await screen.findByText(failedAnswer.content)
    fireEvent.click(screen.getByRole('button', { name: '重试' }))

    expect(await screen.findByText('重试中…')).toBeTruthy()
    // 按钮本身没了——防重复提交不靠"点了没反应"
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull()
    // 整页的发送位置换成了停止，用户随时能收回这次重试
    expect(screen.getByRole('button', { name: '停止生成' })).toBeTruthy()
  })
})

// 【失败的重新生成不该在旧回答上留下「已被重新生成」】
//
// 这个标记原来写在**请求发出之前**：一次根本没到服务端的重新生成（后端没起、
// 代理断了）会在界面上同时留下红色「生成失败」和旧回答下面的「已被重新生成」
// ——两句互相矛盾，而那一轮从没被重新生成过、也没有任何新分支。
describe('重新生成：失败时不改标记（issue #90）', () => {
  it('请求出错时，旧回答不标「已被重新生成」', async () => {
    mockHistory([question, answer])
    // 流没到服务端就报错。
    //
    // 【必须走 onError，不能让假实现 throw】`streamChat` 的真实契约是
    // **自己把错误吃下来交给 onError**（`lib/streamChat.ts` 的 catch：
    // 没被中止就调 onError），只有中止那一条路不调。用一个会 throw 的假实现
    // 测出来的是"假实现和真实现不一样"，不是产品行为。
    // 签名是 (conversationId, text, idempotencyKey, callbacks, signal)——
    // 少写一个参数的话 `callbacks` 收到的是那个幂等键字符串，`onError` 是
    // undefined，报出来的是"类型不对"而不是产品行为。
    vi.mocked(streamChat).mockImplementation(async (_id, _text, _key, callbacks) => {
      callbacks.onError(new Error('连不上服务端'))
      return null
    })

    renderPage()
    await screen.findByText(answer.content)

    fireEvent.click(screen.getByRole('button', { name: '重新生成' }))
    await waitFor(() => expect(vi.mocked(streamChat)).toHaveBeenCalledTimes(1))

    // 报错出现了（这一轮确实失败了）。
    expect(await screen.findByText('生成失败')).toBeTruthy()
    // 而旧回答**没有**被标成"已被重新生成"——它没有被替代过。
    expect(screen.queryByText('已被重新生成')).toBeNull()
  })
})
