// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { useCreateConversation } from '@/hooks/useConversations'

vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))

type Conversation = Schemas['Conversation']

/**
 * 请求体的形状按 useCreateConversation 的签名写，不用契约里的 CreateConversationRequest：
 * 契约那一侧 knowledgeBaseId 是 `string | null`（CreateConversationRequest 允许显式传 null），
 * hook 的入参签名收窄成了 `string | undefined`。两者不一致，直接用契约类型会编译不过——
 * 这是 hook 签名与契约的既有差异，不在本文件的改动范围里。
 */
type CreateConversationInput = { title?: string; knowledgeBaseId?: string }

// openapi-fetch 的返回形状是 { data, error, response }，泛型跟着 schemaPath 走，
// 测试里只关心 data，所以把签名收敛成最松的 Mock 再用。
const postMock = api.POST as unknown as Mock

const KB_ID = '11111111-1111-4111-8111-111111111111'

const savedConversation: Conversation = {
  id: '77777777-7777-4777-8777-777777777777',
  title: '关于 ConGoRAG 的问答',
  knowledgeBaseId: KB_ID,
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

function makeWrapper(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

/**
 * 【这条不是回归测试】issue #50 说每个写操作都没作废列表缓存——这里连"列表"都不存在：
 * useConversations.ts:8-12 的注释写明这一轮没有 GET /api/v1/conversations，
 * contracts/openapi.yaml 里 /api/v1/conversations 也只声明了 post。
 * 没有 list query 可失效，所以"作废缓存"这件事在这里没有可断言的对象，
 * 相应的断言等那个端点补上之后再写。
 *
 * 这条钉住的是 mutation 对外契约的另外两半：请求体的字段名。
 * 调用方 KnowledgeBaseDetailPage.tsx 用 conv.id 跳转到会话页，
 * 字段名或返回形状一变，这两处一起坏掉而且不报错。
 */
describe('useCreateConversation', () => {
  afterEach(() => {
    postMock.mockReset()
  })

  it('按契约发出 { title, knowledgeBaseId }，并把服务端返回的会话交回调用方', async () => {
    postMock.mockResolvedValue({ data: savedConversation, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useCreateConversation(), {
      wrapper: makeWrapper(queryClient),
    })

    const input: CreateConversationInput = {
      title: '关于 ConGoRAG 的问答',
      knowledgeBaseId: KB_ID,
    }

    let returned: Conversation | undefined
    await act(async () => {
      // mutateAsync 的解析值就是页面拿去做 navigate(`/conversations/${conv.id}`) 的那个对象。
      returned = await result.current.mutateAsync(input)
    })

    expect(postMock).toHaveBeenCalledWith('/api/v1/conversations', { body: input })
    expect(returned?.id).toBe(savedConversation.id)
    // 关联的知识库要原样回来——会话页靠它决定发消息时做不做检索增强。
    expect(returned?.knowledgeBaseId).toBe(KB_ID)
  })
})
