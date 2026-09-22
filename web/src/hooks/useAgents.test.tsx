// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import {
  AGENTS_KEY,
  sortAndFilterRuns,
  useAgents,
  useCreateAgent,
  useUpdateAgent,
} from '@/hooks/useAgents'
import type { Agent, AgentRun } from '@/hooks/useAgents'

// 网络层换成可控 mock：本文件要验证的是"创建成功后 Agent 列表缓存里是不是服务端的新数据"。
// GET 的返回值由下面的 serverList 决定，相当于一个会在测试中途改变状态的服务端。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn() } }))

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock
const patchMock = api.PATCH as unknown as Mock

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

/**
 * 编辑 Agent 走的是 PATCH /api/v1/agents/{id}（issue #81）。
 *
 * 【这条钉住的是"整体替换"这个契约】请求体里四个字段一个都不能少：契约的
 * UpdateAgentRequest 把它们全列进了 required，语义是"改完之后的值"。
 * 只发改动过的字段会（按后端的语义）把没发的字段清空——那正是这条用例要防的。
 */
describe('useUpdateAgent', () => {
  beforeEach(() => {
    serverList = [calculator]
    getMock.mockImplementation(() => Promise.resolve({ data: serverList, error: undefined }))
  })

  afterEach(() => {
    getMock.mockReset()
    patchMock.mockReset()
  })

  it('四个字段一起发，成功后列表缓存换成服务端的新数据', async () => {
    const renamed = {
      ...calculator,
      name: '算术助手',
      instruction: '',
      toolNames: [],
      updatedAt: '2026-09-22T00:00:00Z',
    }
    patchMock.mockResolvedValue({ data: renamed, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(
      () => ({ list: useAgents(), update: useUpdateAgent() }),
      { wrapper: makeWrapper(queryClient) },
    )
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true))

    // 服务端：PATCH 落库之后下一次 GET 会带上改好的那条（名字变了、提示词清空）
    serverList = [renamed]
    await act(async () => {
      await result.current.update.mutateAsync({
        id: calculator.id,
        // instruction 是空串而不是省略：清空 system prompt 是这个端点要
        // 表达的东西之一（整体替换语义）。
        body: { name: '算术助手', description: '只做算术', instruction: '', toolNames: [] },
      })
    })

    expect(patchMock).toHaveBeenCalledWith('/api/v1/agents/{id}', {
      params: { path: { id: calculator.id } },
      body: { name: '算术助手', description: '只做算术', instruction: '', toolNames: [] },
    })
    await waitFor(() => expect(cachedAgents(queryClient)).toEqual([renamed]))
    expect(cachedAgents(queryClient)[0].instruction).toBe('')
  })
})

/** 运行历史的 fixture。都是终态——非终态会让 useAgentRuns 的轮询把测试吊住。 */
function run(id: string, status: AgentRun['status'], createdAt: string): AgentRun {
  return {
    id,
    agentId: calculator.id,
    status,
    currentStep: 1,
    input: `任务 ${id}`,
    output: '',
    createdAt,
    updatedAt: createdAt,
  }
}

/**
 * 运行历史的排序与筛选（issue #92 的另一半）。
 *
 * 【这是纯函数的契约固定测试，不是回归测试】它钉的是三件事：筛选按状态、
 * 排序按**时间戳**（不是字符串）、以及默认顺序就是服务器顺序（不再排一遍）。
 * 拿字符串比时间会在跨时区偏移的数据上悄悄错序——所以这里故意用 +08:00
 * 与 Z 两种偏移混着写。
 */
describe('sortAndFilterRuns', () => {
  const newest = run('a', 'completed', '2026-09-22T10:00:00+08:00') // = 02:00Z
  const middle = run('b', 'failed', '2026-09-21T20:00:00Z')
  const oldest = run('c', 'interrupted', '2026-09-21T00:00:00Z')

  // 服务器的顺序：created_at 倒序
  const serverOrder = [newest, middle, oldest]

  it('默认（最新在前）保持服务器给的顺序，不重排', () => {
    expect(sortAndFilterRuns(serverOrder, 'newest', 'all')).toEqual(serverOrder)
  })

  it('最早在前按时间戳重排，跨时区偏移也正确', () => {
    // 按字符串比的话 '2026-09-22T10:00:00+08:00' 会被排到最前面
    // （'2' > '1'），实际它比 middle 更晚。
    expect(sortAndFilterRuns(serverOrder, 'oldest', 'all').map((r) => r.id)).toEqual([
      'c',
      'b',
      'a',
    ])
  })

  it('按状态筛选只留匹配的那些，六态都能筛', () => {
    expect(sortAndFilterRuns(serverOrder, 'newest', 'failed').map((r) => r.id)).toEqual(['b'])
    expect(sortAndFilterRuns(serverOrder, 'newest', 'pending')).toEqual([])
    expect(sortAndFilterRuns(serverOrder, 'newest', 'cancelled')).toEqual([])
  })

  it('不改动入参数组本身', () => {
    const input = [...serverOrder]
    sortAndFilterRuns(input, 'oldest', 'all')
    expect(input).toEqual(serverOrder)
  })
})
