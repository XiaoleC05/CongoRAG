// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import { useAgentRuns } from '@/hooks/useAgents'
import type { AgentRun } from '@/hooks/useAgents'
import { useStartAgentRun } from '@/hooks/useStartAgentRun'
import { streamAgentRun } from '@/lib/streamAgentRun'

// 和 useMessages.test.tsx 同样的换法：网络层换成可控 mock，本文件要验证的
// 是"流结束那一刻缓存有没有被作废、失败的那次 run 会不会出现在历史里"。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))
vi.mock('@/lib/streamAgentRun', () => ({ streamAgentRun: vi.fn() }))

const getMock = api.GET as unknown as Mock

const AGENT_ID = 'a-1'

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

function cachedRuns(queryClient: QueryClient) {
  return queryClient.getQueryData<AgentRun[]>(['agent-runs', AGENT_ID]) ?? []
}

describe('useStartAgentRun', () => {
  afterEach(() => {
    getMock.mockReset()
    vi.mocked(streamAgentRun).mockReset()
  })

  // 【这条是 #26 的回归测试】作废缓存原来只写在 case 'done' 里，而 error 帧
  // 和"流没发 done 就关了"都不走那条分支。运行列表的轮询条件是"缓存里已有
  // pending/running 的行"——刚失败的那次 run 从来没进过那份缓存，于是没有任何
  // 机制会重新拉列表：后端已经把这一行落库并置为 failed，界面上"历史运行"
  // 却一直空着，只有重新挂载页面才会出现。
  it('运行失败也要作废运行列表缓存，失败的 run 会出现在历史里', async () => {
    let persisted: AgentRun[] = []
    getMock.mockImplementation(() => Promise.resolve({ data: persisted, error: undefined }))
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
    getMock.mockImplementation(() => Promise.resolve({ data: persisted, error: undefined }))
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
})
