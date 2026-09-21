// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import { AGENTS_KEY, useAgents, useCreateAgent } from '@/hooks/useAgents'
import type { Agent } from '@/hooks/useAgents'

// 网络层换成可控 mock：本文件要验证的是"创建成功后 Agent 列表缓存里是不是服务端的新数据"。
// GET 的返回值由下面的 serverList 决定，相当于一个会在测试中途改变状态的服务端。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock

const calculator: Agent = {
  id: '55555555-5555-4555-8555-555555555555',
  name: '计算助手',
  description: '只做算术',
  instruction: '你是一个算术助手。',
  toolNames: ['calculator'],
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

const searcher: Agent = {
  id: '66666666-6666-4666-8666-666666666666',
  name: '检索助手',
  description: '查知识库',
  instruction: '你是一个检索助手。',
  toolNames: ['knowledge_search'],
  createdAt: '2026-09-21T00:00:01Z',
  updatedAt: '2026-09-21T00:00:01Z',
}

/** 服务端当前的 Agent 列表，测试在两个阶段之间改写它。 */
let serverList: Agent[]

function makeWrapper(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

/**
 * 同时挂 useAgents 和 useCreateAgent，模拟 AgentsPage 的样子：列表 query 有活跃观察者，
 * 所以 invalidateQueries（默认 refetchType: 'active'）真的会触发重取，
 * 而不是只把缓存标记为 stale。
 */
function renderAgents(queryClient: QueryClient) {
  return renderHook(
    () => ({ list: useAgents(), create: useCreateAgent() }),
    { wrapper: makeWrapper(queryClient) },
  )
}

function cachedAgents(queryClient: QueryClient) {
  return queryClient.getQueryData<Agent[]>(AGENTS_KEY) ?? []
}

/**
 * 【这条不是回归测试】issue #50 的前提在这里同样是错的：useAgents.ts:69 的
 * onSuccess 一直在 invalidateQueries({ queryKey: AGENTS_KEY })，今天跑就是绿的。
 *
 * 它钉住的是缓存 key 契约：断言走模块导出的 AGENTS_KEY。失效侧若写成别的字面量
 * （或漏掉 onSuccess），缓存会停在旧列表上，这条立刻变红——那正是
 * web/README.md §1 说的"拼错一处不会报错，只会让界面点了没反应"。
 */
describe('useCreateAgent', () => {
  beforeEach(() => {
    serverList = [calculator]
    getMock.mockImplementation(() => Promise.resolve({ data: serverList, error: undefined }))
  })

  afterEach(() => {
    getMock.mockReset()
    postMock.mockReset()
  })

  it('创建成功后列表缓存换成服务端的新列表（含新建的 Agent）', async () => {
    postMock.mockResolvedValue({ data: searcher, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderAgents(queryClient)
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true))
    expect(cachedAgents(queryClient)).toEqual([calculator])

    // 模拟服务端：POST 落库之后，下一次 GET 会带上新建的那条。
    serverList = [calculator, searcher]
    await act(async () => {
      await result.current.create.mutateAsync({
        name: searcher.name,
        description: searcher.description,
        instruction: searcher.instruction,
        toolNames: searcher.toolNames,
      })
    })

    await waitFor(() => expect(cachedAgents(queryClient)).toEqual([calculator, searcher]))
    expect(cachedAgents(queryClient).map((a) => a.name)).toContain('检索助手')
  })
})
