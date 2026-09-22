// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { Schemas } from '@congorag/api-client'
import { api } from '@congorag/api-client'
import AgentDetailPage from '@/pages/AgentDetailPage'
import type { ChatEvent, StreamChatCallbacks } from '@/lib/streamChat'
import { streamAgentRun } from '@/lib/streamAgentRun'

type Agent = Schemas['Agent']

vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))
vi.mock('@/lib/streamAgentRun', () => ({ streamAgentRun: vi.fn() }))

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock

const AGENT_ID = '11111111-1111-4111-8111-111111111111'
const RUN_ID = '22222222-2222-4222-8222-222222222222'

const agent: Agent = {
  id: AGENT_ID,
  name: '算数助手',
  description: '',
  instruction: '',
  toolNames: ['calculator'],
  createdAt: '2026-09-22T00:00:00Z',
  updatedAt: '2026-09-22T00:00:00Z',
}

// jsdom 不实现 scrollIntoView，而这一页在时间线变化时会调它（滚到底部）。
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
})

function mockAgent() {
  getMock.mockImplementation((path: string) => {
    if (path === '/api/v1/agents/{id}') return Promise.resolve({ data: agent, error: undefined })
    if (path === '/api/v1/agents/{id}/runs') {
      return Promise.resolve({ data: { items: [], nextCursor: null }, error: undefined })
    }
    throw new Error(`用例没预料到的 GET ${path}`)
  })
}

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/agents/${AGENT_ID}`]}>
        <Routes>
          <Route path="/agents/:id" element={<AgentDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/** 被捕获的那一次运行——用例用它按自己的节奏发帧。 */
type Captured = { callbacks?: StreamChatCallbacks; signal?: AbortSignal }
let captured: Captured = {}

/**
 * 把这次运行的流交给用例自己驱动：拿到 callbacks 之后想发什么帧就发什么帧，
 * 想什么时候发就什么时候发——"首帧 run_started 还没到的那一瞬"这种窗口，
 * 只有这样才能摆出来。
 */
function captureRun() {
  captured = {}
  vi.mocked(streamAgentRun).mockImplementation((_agentId, _input, callbacks, signal) => {
    captured.callbacks = callbacks
    captured.signal = signal
    // 流一直开着，直到被中止。
    return new Promise<void>((resolve) => {
      signal?.addEventListener('abort', () => resolve())
    })
  })
}

function emit(event: ChatEvent) {
  captured.callbacks?.onEvent(event)
}

/** 启动一次运行（填输入框 + 点运行）。 */
function startRun(input = '算一下 1+1') {
  fireEvent.change(screen.getByPlaceholderText('给这个 Agent 一个任务…'), {
    target: { value: input },
  })
  fireEvent.click(screen.getByRole('button', { name: '运行' }))
}

/** 取消端点返回的是**取消生效之后**的那条 run。 */
function cancelledRun(): Schemas['AgentRun'] {
  return {
    id: RUN_ID,
    agentId: AGENT_ID,
    status: 'cancelled',
    currentStep: 1,
    input: '算一下 1+1',
    output: '',
    createdAt: '2026-09-22T00:00:00Z',
    updatedAt: '2026-09-22T00:00:01Z',
  }
}

afterEach(() => {
  getMock.mockReset()
  postMock.mockReset()
  vi.mocked(streamAgentRun).mockReset()
  captured = {}
  cleanup()
})

describe('取消运行（issue #79）', () => {
  it('run_started 到达之前取消按钮是禁用的；到了之后用它按 runId 发 cancel', async () => {
    mockAgent()
    captureRun()
    postMock.mockResolvedValue({ data: cancelledRun(), error: undefined })

    renderPage()
    await screen.findByText(agent.name)

    startRun()

    // 首帧 run_started 还没来：没有 runId，没有端点可发，按钮禁用。
    const cancel = await screen.findByRole('button', { name: '取消运行' })
    expect((cancel as HTMLButtonElement).disabled).toBe(true)
    // 发送按钮已经被它替换掉了
    expect(screen.queryByRole('button', { name: '运行' })).toBeNull()

    // 首帧到达（协议保证它是流的第一帧），runId 就在 data 里。
    emit({ type: 'run_started', id: null, data: { runId: RUN_ID } })
    await waitFor(() => expect((cancel as HTMLButtonElement).disabled).toBe(false))

    fireEvent.click(cancel)

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith('/api/v1/runs/{runId}/cancel', {
        params: { path: { runId: RUN_ID } },
      }),
    )
    // 取消之后本地这条流也掐断（后端那边已经收尾，再等没有意义）。
    await waitFor(() => expect(captured.signal?.aborted).toBe(true))
    // 【取消不是失败】页面上不该出现"运行失败"的报错。
    expect(screen.queryByText('运行失败')).toBeNull()
  })

  it('取消之后徽章显示"已取消"，用的是后端返回的那条 run 的状态', async () => {
    mockAgent()
    captureRun()
    postMock.mockResolvedValue({ data: cancelledRun(), error: undefined })

    renderPage()
    await screen.findByText(agent.name)
    startRun()

    emit({ type: 'run_started', id: null, data: { runId: RUN_ID } })
    fireEvent.click(await screen.findByRole('button', { name: '取消运行' }))

    // 六态里的 cancelled 是一个真实状态，和 failed 分开显示。
    expect(await screen.findByText('已取消')).toBeTruthy()
  })
})

describe('运行时间线里的工具卡片与状态（issue #80）', () => {
  it('tool_call / tool_result 渲染成折叠卡片，工具名与结果都在', async () => {
    mockAgent()
    captureRun()

    renderPage()
    await screen.findByText(agent.name)
    startRun()

    emit({ type: 'run_started', id: null, data: { runId: RUN_ID } })
    // 正在跑：状态徽章说"运行中"
    await screen.findByText('运行中')

    emit({ type: 'tool_call', id: 1, data: { id: 'call-1', name: 'calculator', args: { expr: '1+1' } } })
    const card = await screen.findByRole('button', { expanded: false })
    expect(card.textContent).toContain('calculator')
    // 还没收到结果时卡片说"运行中"（与徽章共用同一份六态映射）
    expect(card.textContent).toContain('运行中')

    emit({ type: 'tool_result', id: 2, data: { id: 'call-1', result: { answer: 2 } } })

    await waitFor(() => expect(screen.getByText('已完成')).toBeTruthy())
    // 默认收起：参数与结果都不在 DOM 里
    expect(screen.queryByText('参数')).toBeNull()

    fireEvent.click(screen.getByRole('button', { expanded: false }))
    expect(screen.getByText('参数')).toBeTruthy()
    expect(screen.getByText('结果')).toBeTruthy()
    expect(screen.getByRole('button', { name: '复制 calculator 的结果' })).toBeTruthy()
  })

  it('工具卡片按到达顺序排在时间线上', async () => {
    mockAgent()
    captureRun()

    renderPage()
    await screen.findByText(agent.name)
    startRun()
    emit({ type: 'run_started', id: null, data: { runId: RUN_ID } })
    emit({ type: 'tool_call', id: 1, data: { id: 'c1', name: 'first_tool', args: {} } })
    emit({ type: 'tool_call', id: 2, data: { id: 'c2', name: 'second_tool', args: {} } })

    // 时间线按到达顺序排列——卡片与 agent_run_steps 的 seq 一一对应，
    // 顺序错了就等于把轨迹讲反了。
    await waitFor(() =>
      expect(screen.getAllByRole('button', { expanded: false })).toHaveLength(2),
    )
    const names = screen
      .getAllByRole('button', { expanded: false })
      .map((b) => b.textContent ?? '')
    expect(names[0]).toContain('first_tool')
    expect(names[1]).toContain('second_tool')
  })
})
