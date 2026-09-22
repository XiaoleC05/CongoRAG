// @vitest-environment jsdom
import type { InfiniteData } from '@tanstack/react-query'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { api } from '@congorag/api-client'
import { useAgentRuns } from '@/hooks/useAgents'
import type { AgentRun } from '@/hooks/useAgents'
import { useStartAgentRun } from '@/hooks/useStartAgentRun'
import type { Page } from '@/lib/pagination'
import { streamAgentRun } from '@/lib/streamAgentRun'

// 和 useMessages.test.tsx 同样的换法：网络层换成可控 mock，本文件要验证的
// 是"流结束那一刻缓存有没有被作废、失败的那次 run 会不会出现在历史里"，
// 以及取消走的是哪个端点、拿不到 runId 时会不会发请求。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))
vi.mock('@/lib/streamAgentRun', () => ({ streamAgentRun: vi.fn() }))
// 取消失败（409）按 §16 走 toast：这里只关心"提示有没有出去"，不渲染 Toaster。
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn(), dismiss: vi.fn() } }))

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock

const AGENT_ID = 'a-1'
const RUN_ID = '55555555-5555-4555-8555-555555555555'

/** 失败的 run 同样会落库——它必须出现在"历史运行"里。 */
const failedRun: AgentRun = {
  id: '33333333-3333-4333-8333-333333333333',
  agentId: AGENT_ID,
  status: 'failed',
  currentStep: 0,
  input: '算一下 1/0',
  output: '',
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:01Z',
}

function makeWrapper(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

/**
 * 同时挂 useStartAgentRun 和 useAgentRuns，模拟 AgentDetailPage 的样子：
 * 运行列表有活跃观察者，所以 invalidateQueries 真的会触发重取。
 */
function renderAgent(queryClient: QueryClient) {
  return renderHook(
    () => ({ runs: useAgentRuns(AGENT_ID), runner: useStartAgentRun(AGENT_ID) }),
    { wrapper: makeWrapper(queryClient) },
  )
}

// 分页（issue #45）之后缓存形状是 {pages, pageParams}，每一页是
// {items, nextCursor}。读的时候摊平。
function cachedRuns(queryClient: QueryClient) {
  const data = queryClient.getQueryData<InfiniteData<Page<AgentRun>>>(['agent-runs', AGENT_ID])
  return data ? data.pages.flatMap((page) => page.items) : []
}

describe('useStartAgentRun', () => {
  afterEach(() => {
    getMock.mockReset()
    postMock.mockReset()
    vi.mocked(streamAgentRun).mockReset()
    vi.mocked(toast.error).mockReset()
  })

  // 【这条是 #26 的回归测试】作废缓存原来只写在 case 'done' 里，而 error 帧
  // 和"流没发 done 就关了"都不走那条分支。运行列表的轮询条件是"缓存里已有
  // pending/running 的行"——刚失败的那次 run 从来没进过那份缓存，于是没有任何
  // 机制会重新拉列表：后端已经把这一行落库并置为 failed，界面上"历史运行"
  // 却一直空着，只有重新挂载页面才会出现。
  it('运行失败也要作废运行列表缓存，失败的 run 会出现在历史里', async () => {
    let persisted: AgentRun[] = []
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: persisted, nextCursor: null }, error: undefined }),
    )
    vi.mocked(streamAgentRun).mockImplementation(async (_agentId, _input, callbacks) => {
      // 模拟后端：run 先落库，然后流里发一个 error 帧，没有 done。
      persisted = [failedRun]
      callbacks.onEvent({
        type: 'error',
        id: 1,
        data: { type: 'internal_error', detail: '工具调用失败' },
      })
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))
    expect(cachedRuns(queryClient)).toEqual([])

    await act(async () => {
      await result.current.runner.start('算一下 1/0')
    })

    // 失败路径作废了缓存 -> 重新拉取 -> 失败的 run 出现。
    // 修复前（只在 done 里作废）这里仍然是空数组。
    await waitFor(() => expect(cachedRuns(queryClient)).toEqual([failedRun]))
    expect(result.current.runner.isRunning).toBe(false)
  })

  // 同一件事的另一半：连接层直接报错（onError，没有 error 帧）也必须作废缓存。
  // 这是"流没发 done 就断了"的形态，也就是最常见的失败。
  it('连接层直接失败时同样作废运行列表缓存', async () => {
    let persisted: AgentRun[] = []
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: persisted, nextCursor: null }, error: undefined }),
    )
    vi.mocked(streamAgentRun).mockImplementation(async (_agentId, _input, callbacks) => {
      persisted = [failedRun]
      callbacks.onError(new Error('连接中断'))
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))

    await act(async () => {
      await result.current.runner.start('算一下 1/0')
    })

    await waitFor(() => expect(cachedRuns(queryClient)).toEqual([failedRun]))
    // 错误要能被页面读到，不能只吞进缓存里。
    expect(result.current.runner.runError).toBeTruthy()
    expect(result.current.runner.isRunning).toBe(false)
  })

  // ── 取消（issue #79） ────────────────────────────────────────────────
  //
  // 契约里取消是运行层的动作：POST /api/v1/runs/{runId}/cancel 返回取消
  // **生效之后**的那条 run。而 runId 在此之前根本不在流里——它只出现在
  // 首帧 run_started 上，所以这一组用例同时钉住"id 从哪来"。

  it('run_started 给出 runId；cancel 用它发请求并采用后端返回的终态', async () => {
    const cancelledRun: AgentRun = { ...failedRun, status: 'cancelled' }
    let signal: AbortSignal | undefined

    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: [], nextCursor: null }, error: undefined }),
    )
    postMock.mockResolvedValue({ data: cancelledRun, error: undefined })

    vi.mocked(streamAgentRun).mockImplementation((_agentId, _input, callbacks, abort) => {
      signal = abort
      callbacks.onEvent({ type: 'run_started', id: 1, data: { runId: RUN_ID } })
      // 流一直开着：取消之前这次运行还在跑。
      return new Promise<void>((resolve) => {
        abort?.addEventListener('abort', () => resolve())
      })
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))

    act(() => {
      void result.current.runner.start('算一下 1/0')
    })
    await waitFor(() => expect(result.current.runner.runId).toBe(RUN_ID))

    await act(async () => {
      await result.current.runner.cancel()
    })

    expect(postMock).toHaveBeenCalledWith('/api/v1/runs/{runId}/cancel', {
      params: { path: { runId: RUN_ID } },
    })
    // 【取消不是失败】用户按的是"我不要了"，页面上不该出现"运行失败"。
    expect(result.current.runner.runError).toBeNull()
    // 状态用的是后端返回的那条 run（cancelled），不是前端自己推的。
    expect(result.current.runner.status).toBe('cancelled')
    // 本地这条流也掐掉，不空等一条已经没有意义的连接——而掐断不算错误。
    expect(signal?.aborted).toBe(true)
  })

  it('首帧 run_started 还没到时没有 runId，cancel 不发请求', async () => {
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: [], nextCursor: null }, error: undefined }),
    )
    vi.mocked(streamAgentRun).mockImplementation(
      () => new Promise<void>(() => {}), // 连首帧都还没发出来
    )

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))

    act(() => {
      void result.current.runner.start('算一下 1/0')
    })

    expect(result.current.runner.runId).toBeNull()
    await act(async () => {
      await result.current.runner.cancel()
    })

    // 没有 id 就没有端点可发——静默什么都不做，比发一个假请求好。
    expect(postMock).not.toHaveBeenCalled()
  })

  it('取消被 409 拒绝时不误标 cancelled：给提示，并让列表拉一次真实状态', async () => {
    let signal: AbortSignal | undefined
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: [], nextCursor: null }, error: undefined }),
    )
    // 契约：已经跑到终态的 run 不可取消（409 conflict）。
    postMock.mockResolvedValue({
      error: {
        type: 'conflict',
        title: '状态冲突',
        status: 409,
        detail: 'run is completed',
      },
      response: new Response(),
    })

    vi.mocked(streamAgentRun).mockImplementation((_agentId, _input, callbacks, abort) => {
      signal = abort
      callbacks.onEvent({ type: 'run_started', id: 1, data: { runId: RUN_ID } })
      return new Promise<void>(() => {})
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))

    act(() => {
      void result.current.runner.start('算一下 1/0')
    })
    await waitFor(() => expect(result.current.runner.runId).toBe(RUN_ID))

    await act(async () => {
      await result.current.runner.cancel()
    })

    // 不能因为"点了取消"就把界面画成已取消——那正是"静默说谎"。
    expect(result.current.runner.status).not.toBe('cancelled')
    // 也不用去掐断流：这次运行并不归这次点击处置，让它自己收场。
    expect(signal?.aborted).toBe(false)
    // 提示走 toast（§16：写操作失败由 errorPresentation 决定形式）。
    expect(vi.mocked(toast.error)).toHaveBeenCalled()
  })

  it('流没走到终态就断了时状态是 interrupted——v4.0 起它真的会落库', async () => {
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: [], nextCursor: null }, error: undefined }),
    )
    vi.mocked(streamAgentRun).mockImplementation(async (_agentId, _input, callbacks) => {
      callbacks.onEvent({ type: 'run_started', id: 1, data: { runId: RUN_ID } })
      // 没有 done、没有 error：连接断了。
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))

    await act(async () => {
      await result.current.runner.start('算一下 1/0')
    })

    // 客户端断开时后端写的正是 interrupted（不是 failed），徽章照实显示。
    await waitFor(() => expect(result.current.runner.status).toBe('interrupted'))
  })

  // 回归（issue #102）：组件卸载（离开页面、切走）之后事件流还在被消费，
  // 而「取消」按钮所在的页面已经没了——用户没有任何入口叫停它，只能看着
  // 它烧额度跑完。
  it('卸载时中止在途的运行流', async () => {
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: [], nextCursor: null }, error: undefined }),
    )
    let abort: AbortSignal | undefined
    vi.mocked(streamAgentRun).mockImplementation((_agentId, _input, callbacks, signal) => {
      abort = signal
      callbacks.onEvent({ type: 'run_started', id: 1, data: { runId: RUN_ID } })
      // 流一直开着：卸载之前这次运行还在跑。
      return new Promise<void>(() => {})
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result, unmount } = renderAgent(queryClient)
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true))

    act(() => {
      void result.current.runner.start('算一下 1/0')
    })
    await waitFor(() => expect(result.current.runner.runId).toBe(RUN_ID))
    expect(abort?.aborted).toBe(false)

    unmount()
    expect(abort?.aborted).toBe(true)
  })
})
