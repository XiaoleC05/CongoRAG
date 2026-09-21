// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import {
  KNOWLEDGE_BASES_KEY,
  useKnowledgeBaseMutations,
  useKnowledgeBases,
} from '@/hooks/useKnowledgeBases'

// 网络层换成可控 mock：本文件要验证的是"mutate 成功后列表缓存里是不是服务端的新数据"，
// 真实网络只会让这件事更难摆出来。GET 的返回值由下面的 serverList 决定，
// 相当于一个会在测试中途改变状态的服务端。
vi.mock('@congorag/api-client', () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn(), DELETE: vi.fn() },
}))

type KnowledgeBase = Schemas['KnowledgeBase']

// openapi-fetch 的返回形状是 { data, error, response }，泛型跟着 schemaPath 走，
// 测试里只关心 data，所以把签名收敛成最松的 Mock 再用。
const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock
const patchMock = api.PATCH as unknown as Mock
const deleteMock = api.DELETE as unknown as Mock

const kbA: KnowledgeBase = {
  id: '11111111-1111-4111-8111-111111111111',
  name: '产品手册',
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

const kbB: KnowledgeBase = {
  id: '22222222-2222-4222-8222-222222222222',
  name: '新建的知识库',
  createdAt: '2026-09-21T00:00:01Z',
  updatedAt: '2026-09-21T00:00:01Z',
}

/**
 * 服务端当前的列表。测试在两个阶段之间改写它——GET 的 mock 每次调用时
 * 读的都是这个变量的最新值，于是"写操作之后服务端变了"能被前端看到。
 */
let serverList: KnowledgeBase[]

function makeWrapper(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

/**
 * 同时挂 useKnowledgeBases 和 useKnowledgeBaseMutations，模拟 KnowledgeBasesPage 的样子：
 * 列表 query 有活跃观察者，所以 invalidateQueries（默认 refetchType: 'active'）
 * 真的会触发重取，而不是只把缓存标记为 stale。
 */
function renderPage(queryClient: QueryClient) {
  return renderHook(
    () => ({ list: useKnowledgeBases(), mutations: useKnowledgeBaseMutations() }),
    { wrapper: makeWrapper(queryClient) },
  )
}

function cachedList(queryClient: QueryClient) {
  return queryClient.getQueryData<KnowledgeBase[]>(KNOWLEDGE_BASES_KEY) ?? []
}

/**
 * 【这些不是回归测试】issue #50 的前提在这里是错的：useKnowledgeBases.ts 的
 * create/rename/remove 从一开始就调了 invalidateQueries（见 useKnowledgeBases.ts:44-46
 * 的 invalidateList，以及 :54 / :66 / :77 三处引用），今天跑就是绿的。
 *
 * 它们钉住的是缓存 key 契约：断言走的是模块导出的 KNOWLEDGE_BASES_KEY。
 * 只要读取侧和写入侧的 key 出现分歧（写入侧的 invalidate 用了别的字面量，
 * 比如 ['knowledge-base'] 少个 s），失效就落空、缓存停在旧值上，这几条会立刻变红——
 * 而那正是 web/README.md §1 说的"拼错一处不会报错，只会让界面点了没反应"。
 */
describe('useKnowledgeBaseMutations', () => {
  beforeEach(() => {
    serverList = [kbA]
    getMock.mockImplementation(() => Promise.resolve({ data: serverList, error: undefined }))
  })

  afterEach(() => {
    getMock.mockReset()
    postMock.mockReset()
    patchMock.mockReset()
    deleteMock.mockReset()
  })

  it('创建成功后列表缓存换成服务端的新列表（含新建那条）', async () => {
    postMock.mockResolvedValue({ data: kbB, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderPage(queryClient)
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true))
    expect(cachedList(queryClient)).toEqual([kbA])

    // 模拟服务端：POST 落库之后，下一次 GET 会带上新建的那条。
    serverList = [kbA, kbB]
    await act(async () => {
      await result.current.mutations.create.mutateAsync('新建的知识库')
    })

    await waitFor(() => expect(cachedList(queryClient)).toEqual([kbA, kbB]))
  })

  it('改名成功后列表缓存里是服务端返回的新名字', async () => {
    const renamed: KnowledgeBase = { ...kbA, name: '改名后的手册' }
    patchMock.mockResolvedValue({ data: renamed, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderPage(queryClient)
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true))

    serverList = [renamed]
    await act(async () => {
      await result.current.mutations.rename.mutateAsync({ id: kbA.id, name: renamed.name })
    })

    await waitFor(() => expect(cachedList(queryClient)).toEqual([renamed]))
    expect(cachedList(queryClient)[0]?.name).toBe('改名后的手册')
  })

  it('删除成功后列表缓存里不再有那条', async () => {
    deleteMock.mockResolvedValue({ data: undefined, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderPage(queryClient)
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true))

    serverList = []
    await act(async () => {
      await result.current.mutations.remove.mutateAsync(kbA.id)
    })

    await waitFor(() => expect(cachedList(queryClient)).toEqual([]))
  })
})
