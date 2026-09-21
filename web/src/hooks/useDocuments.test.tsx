// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { documentsKey, useDocumentMutations, useDocuments } from '@/hooks/useDocuments'

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

function cachedDocs(queryClient: QueryClient) {
  return queryClient.getQueryData<Document[]>(documentsKey(KB_ID)) ?? []
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
    getMock.mockImplementation(() => Promise.resolve({ data: serverList, error: undefined }))
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
