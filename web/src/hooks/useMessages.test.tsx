// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import { messagesKey, useMessages, useSendMessage } from '@/hooks/useMessages'
import type { Message } from '@/hooks/useMessages'
import { streamChat } from '@/lib/streamChat'

// api 和 streamChat 都换成可控的 mock：本文件要验证的是"什么写进了缓存、
// 什么时候重新拉取"，真实网络层只会让这两件事更难摆出来。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))
vi.mock('@/lib/streamChat', () => ({ streamChat: vi.fn() }))

// openapi-fetch 的返回形状是 { data, error, response }，泛型跟着 schemaPath 走，
// 测试里只关心 data，所以把签名收敛成最松的 Mock 再用。
const getMock = api.GET as unknown as Mock

const CONVERSATION_ID = 'c-1'

/** 后端在 error 帧之前已经把用户这条消息落库了——重新拉取应该能拿到它。 */
const persistedUserMessage: Message = {
  id: '22222222-2222-4222-8222-222222222222',
  conversationId: CONVERSATION_ID,
  role: 'user',
  content: '你好',
  status: 'completed',
  sequenceNo: 1,
  createdAt: '2026-09-21T00:00:00Z',
}

function makeWrapper(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
}

/**
 * 同时挂 useMessages 和 useSendMessage，模拟 ConversationPage 的样子：
 * messages query 有活跃观察者，所以 invalidateQueries 真的会触发重取。
 */
function renderChat(queryClient: QueryClient) {
  return renderHook(
    () => ({ messages: useMessages(CONVERSATION_ID), sender: useSendMessage(CONVERSATION_ID) }),
    { wrapper: makeWrapper(queryClient) },
  )
}

function cachedMessages(queryClient: QueryClient) {
  return queryClient.getQueryData<Message[]>(messagesKey(CONVERSATION_ID)) ?? []
}

describe('useSendMessage', () => {
  afterEach(() => {
    getMock.mockReset()
    vi.mocked(streamChat).mockReset()
  })

  // 回归：文档注释承诺"发送那一刻先上屏用户消息"，但函数体从不写缓存，
  // 生成期间对话区渲染的还是那份没失效的 ['messages', id]，用户刚敲进去的
  // 问题整个流式过程都不在屏幕上。
  it('发送那一刻就把用户消息插进缓存，不等后端确认', async () => {
    getMock.mockResolvedValue({ data: [], error: undefined })
    // 流不结束：这一段就是"正在生成中"。
    vi.mocked(streamChat).mockImplementation(() => new Promise<void>(() => {}))

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderChat(queryClient)
    await waitFor(() => expect(result.current.messages.isSuccess).toBe(true))

    act(() => {
      void result.current.sender.send('你好')
    })

    const cached = cachedMessages(queryClient)
    expect(cached.map((m) => m.content)).toContain('你好')
    expect(cached.at(-1)?.role).toBe('user')
  })

  // 回归：error 帧之后后端已经把用户消息落库（只把 assistant 那条标 failed），
  // 修复前只有 done 分支作废缓存，失败路径永不重取——界面只剩一句"生成失败"，
  // 用户刚说过的话在屏幕上凭空消失、在库里却存在。
  it('生成失败也要作废消息缓存，重新拉取后用户那条消息仍在', async () => {
    let persisted: Message[] = []
    getMock.mockImplementation(() => Promise.resolve({ data: persisted, error: undefined }))
    vi.mocked(streamChat).mockImplementation(async (_id, _text, _key, callbacks) => {
      // 模拟后端：先落库用户消息，再发 error 帧。
      persisted = [persistedUserMessage]
      callbacks.onEvent({
        type: 'error',
        id: 1,
        data: { type: 'upstream_llm_error', detail: '模型调用失败' },
      })
    })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderChat(queryClient)
    await waitFor(() => expect(result.current.messages.isSuccess).toBe(true))

    await act(async () => {
      await result.current.sender.send('你好')
    })

    // 乐观插入那条（id 带 optimistic- 前缀）被数据库里的那一行替换掉，
    // 说明失败路径确实重新拉取过；修复前缓存里只剩下乐观插入的那条。
    await waitFor(() =>
      expect(cachedMessages(queryClient).map((m) => m.id)).toEqual([persistedUserMessage.id]),
    )
  })

  // 幂等键（issue #37）：每一次 send 都要带一个非空的键，且两次 send 的键
  // 必须不同——键写死的话，第二次提问会命中第一次的记录，服务端不生成新
  // 回答、直接把上一轮的答案补发回来，用户看到的是"发了消息但答案没变"。
  it('每次发送都带一个新的、非空的幂等键', async () => {
    getMock.mockResolvedValue({ data: [], error: undefined })
    vi.mocked(streamChat).mockImplementation(() => new Promise<void>(() => {}))

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderChat(queryClient)
    await waitFor(() => expect(result.current.messages.isSuccess).toBe(true))

    await act(async () => {
      void result.current.sender.send('第一问')
      void result.current.sender.send('第二问')
    })

    const keys = vi.mocked(streamChat).mock.calls.map((c) => c[2])
    expect(keys).toHaveLength(2)
    for (const k of keys) {
      expect(typeof k).toBe('string')
      expect(k.length).toBeGreaterThan(0)
    }
    expect(keys[0]).not.toBe(keys[1])
  })
})
