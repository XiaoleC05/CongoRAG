// @vitest-environment jsdom
import type { InfiniteData } from '@tanstack/react-query'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { documentsKey, useDocumentMutations, useDocuments } from '@/hooks/useDocuments'
import type { Page } from '@/lib/pagination'

// 网络层换成可控 mock：本文件要验证的是"mutate 成功后文档列表缓存里是不是服务端的新数据"。
// GET 的返回值由下面的 serverList 决定，相当于一个会在测试中途改变状态的服务端。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() } }))

type Document = Schemas['Document']

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock
const deleteMock = api.DELETE as unknown as Mock

const KB_ID = 'kb-1'

/**
 * 【每条 fixture 必须是终态（ready / failed）】useDocuments.ts:32-39 的
 * refetchInterval 只要看到队列里还有 queued/processing 的行就会每 2 秒重取一次，
 * 计时器会把测试进程一直吊住（`retry: false` 管的是失败重试，管不到轮询）。
 */
const readyDoc: Document = {
  id: '33333333-3333-4333-8333-333333333333',
  knowledgeBaseId: KB_ID,
  filename: '手册.md',
  status: 'ready',
  byteSize: 2048,
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:05Z',
}

const failedDoc: Document = {
  id: '44444444-4444-4444-8444-444444444444',
  knowledgeBaseId: KB_ID,
  filename: '坏文件.txt',
  status: 'failed',
  byteSize: 128,
  createdAt: '2026-09-21T00:00:01Z',
  updatedAt: '2026-09-21T00:00:06Z',
}

/** 服务端当前的文档列表，测试在两个阶段之间改写它。 */
let serverList: Document[]

function makeWrapper(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

/**
 * 同时挂 useDocuments 和 useDocumentMutations，模拟 KnowledgeBaseDetailPage 的样子：
 * 文档列表 query 有活跃观察者，所以 invalidateQueries（默认 refetchType: 'active'）
 * 真的会触发重取，而不是只把缓存标记为 stale。
 */
function renderDetail(queryClient: QueryClient) {
  return renderHook(
    () => ({ docs: useDocuments(KB_ID), mutations: useDocumentMutations(KB_ID) }),
    { wrapper: makeWrapper(queryClient) },
  )
}

// 分页（issue #45）之后缓存形状是 {pages, pageParams}，每一页是
// {items, nextCursor}。读的时候摊平——断言仍然只看「文档数组」这一件事。
function cachedDocs(queryClient: QueryClient) {
  const data = queryClient.getQueryData<InfiniteData<Page<Document>>>(documentsKey(KB_ID))
  return data ? data.pages.flatMap((page) => page.items) : []
}

/**
 * 【这些不是回归测试】issue #50 的前提在这里也是错的：useDocuments.ts:52-53 的
 * invalidateList 在 upload 与 remove 两处都调了（:80 / :90），今天跑就是绿的。
 *
 * 它们钉住的是缓存 key 契约：queryKey 由 documentsKey(kbId) 生成，失效也走同一个
 * 函数。key 里丢掉 kbId（或写成别的形状）会让失效落空，缓存停在旧值上，
 * 这几条会立刻变红——那正是 web/README.md §1 说的"拼错一处不会报错，
 * 只会让界面点了没反应"。
 */
describe('useDocumentMutations', () => {
  beforeEach(() => {
    serverList = [readyDoc]
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: serverList, nextCursor: null }, error: undefined }),
    )
  })

  afterEach(() => {
    getMock.mockReset()
    postMock.mockReset()
    deleteMock.mockReset()
  })

  it('上传成功后列表缓存换成服务端的新列表（含刚上传那条）', async () => {
    // 202：上传只是"排上了队"，后端不回文档体。
    postMock.mockResolvedValue({ data: undefined, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderDetail(queryClient)
    await waitFor(() => expect(result.current.docs.isSuccess).toBe(true))
    expect(cachedDocs(queryClient)).toEqual([readyDoc])

    // 模拟服务端：上传落库之后，下一次 GET 会带上那一条（终态，不触发轮询）。
    serverList = [readyDoc, failedDoc]
    await act(async () => {
      await result.current.mutations.upload.mutateAsync(new File(['# 标题'], '坏文件.txt'))
    })

    await waitFor(() => expect(cachedDocs(queryClient)).toEqual([readyDoc, failedDoc]))
  })

  it('删除成功后列表缓存里不再有那条文档', async () => {
    deleteMock.mockResolvedValue({ data: undefined, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderDetail(queryClient)
    await waitFor(() => expect(result.current.docs.isSuccess).toBe(true))

    serverList = []
    await act(async () => {
      await result.current.mutations.remove.mutateAsync(readyDoc.id)
    })

    await waitFor(() => expect(cachedDocs(queryClient)).toEqual([]))
  })
})

/**
 * 分页（issue #45）。
 *
 * 【这一组钉的是「能不能继续加载」】分页做错的方式都很安静：nextCursor 永远
 * 非空会让「加载更多」一直亮着、点了重复拿同一页；永远为空则用户再也看不到
 * 更早的文档。两种都不报错。
 */
describe('useDocuments 分页', () => {
  afterEach(() => {
    getMock.mockReset()
  })

  it('第一页带游标时还能继续加载，取到 null 就到底了', async () => {
    const calls: string[] = []
    getMock.mockImplementation(async (_path: string, opts: unknown) => {
      const cursor = (opts as { params?: { query?: { cursor?: string } } })?.params?.query?.cursor
      calls.push(cursor ?? '')
      if (!cursor) {
        return { data: { items: [readyDoc], nextCursor: 'CURSOR-1' }, error: undefined }
      }
      return { data: { items: [failedDoc], nextCursor: null }, error: undefined }
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useDocuments(KB_ID), { wrapper: makeWrapper(queryClient) })

    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.hasNextPage).toBe(true)
    expect(cachedDocs(queryClient).map((d) => d.id)).toEqual([readyDoc.id])

    await act(async () => {
      await result.current.fetchNextPage()
    })

    // 两页拼起来是完整的列表，且第二页确实带着第一页给的游标去请求。
    expect(calls).toEqual(['', 'CURSOR-1'])

    // 【为什么要 waitFor 而不是直接断言】缓存更新和组件重渲染是两步：
    // 缓存里已经是两页了，但 result.current 可能还停在上一帧。
    await waitFor(() =>
      expect(cachedDocs(queryClient).map((d) => d.id)).toEqual([readyDoc.id, failedDoc.id]),
    )

    // nextCursor 为 null → 没有下一页，「加载更多」据此收起来。
    await waitFor(() => expect(result.current.hasNextPage).toBe(false))
  })

  it('一次只有一页且 nextCursor 为 null 时，不该有下一页', async () => {
    getMock.mockImplementation(() =>
      Promise.resolve({ data: { items: [readyDoc], nextCursor: null }, error: undefined }),
    )

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useDocuments(KB_ID), { wrapper: makeWrapper(queryClient) })

    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.hasNextPage).toBe(false)
    expect(result.current.isFetchingNextPage).toBe(false)
  })
})
