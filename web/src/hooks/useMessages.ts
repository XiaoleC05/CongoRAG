import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useRef, useState } from 'react'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { streamChat } from '@/lib/streamChat'

type Message = Schemas['Message']

export const messagesKey = (conversationId: string) => ['messages', conversationId]

/** 读：一个会话的历史消息（页面刷新重载用）。 */
export function useMessages(conversationId: string) {
  return useQuery({
    queryKey: messagesKey(conversationId),
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/conversations/{id}/messages', {
        params: { path: { id: conversationId } },
      })
      if (error) throw error
      return data
    },
  })
}

/** 展示层用的引用：从 citation 事件的 payload 摊平出来，配到具体某条消息上。 */
export type DisplayCitation = {
  chunkId: string
  documentId: string
  filename: string
  snippet: string
  score: number
}

/**
 * 乐观插入的那条用户消息的 id。
 *
 * 它只在"发出请求到作废重取"这一段里存在——id 是本地编的，不保证和后端
 * 的 uuid 撞不上，所以必须带前缀，然后被重新拉取的列表整体替换掉。
 */
let optimisticSeq = 0

function nextOptimisticId() {
  optimisticSeq += 1
  return `optimistic-${optimisticSeq}`
}

/**
 * 发消息 + 消费流式回答。
 *
 * 【为什么不用 useMutation】这不是一次性的请求-响应，是一个持续到 done
 * 事件才结束的过程,中途要不断更新"正在生成中的这条消息内容"——
 * TanStack Query 的 mutation 状态模型（pending/error/success 三态）
 * 表达不了"正在流式追加内容"这个中间态，所以用普通的 useState 自己管。
 *
 * 【乐观更新用户消息】发送那一刻就把用户的提问插进 messages 缓存，不等
 * 后端确认——技术方案 §4.2 的乐观更新模式："发送消息：先上屏用户消息，
 * SSE 完成或失败后确认/回滚"。不插的话，生成期间对话区渲染的是尚未失效的
 * ['messages', id] 缓存，用户刚敲进去的问题在整个流式过程里都不在屏幕上。
 *
 * 【assistant 那条不插缓存】生成中的回答由 streamingContent 这个 state
 * 单独渲染成一条 pending 气泡（见 ConversationPage），再往缓存里放一条
 * 空的 assistant 占位会让同一轮问答出现两个气泡。
 */
export function useSendMessage(conversationId: string) {
  const queryClient = useQueryClient()
  const [pendingCitations, setPendingCitations] = useState<DisplayCitation[]>([])
  const [streamingContent, setStreamingContent] = useState('')
  const [isStreaming, setIsStreaming] = useState(false)
  const [streamError, setStreamError] = useState<unknown>(null)

  // 用 ref 存正在拼接的完整内容——streamingContent 这个 state 用来渲染,
  // 但连续的 setState 调用之间读不到彼此的最新值,拼接内容必须用 ref。
  const contentRef = useRef('')

  const send = useCallback(
    async (text: string) => {
      setIsStreaming(true)
      setStreamError(null)
      setStreamingContent('')
      setPendingCitations([])
      contentRef.current = ''

      const key = messagesKey(conversationId)

      // 乐观插入：内容、顺序都取自用户刚敲的这一下，不等后端返回 uuid 和
      // sequence_no。随后的作废重取会用数据库里的那一行把它替换掉。
      queryClient.setQueryData<Message[]>(key, (old) => [
        ...(old ?? []),
        {
          id: nextOptimisticId(),
          conversationId,
          role: 'user',
          content: text,
          status: 'completed',
          sequenceNo: (old?.[old.length - 1]?.sequenceNo ?? 0) + 1,
          createdAt: new Date().toISOString(),
        },
      ])

      try {
        await streamChat(conversationId, text, {
          onEvent: (event) => {
            switch (event.type) {
              case 'citation':
                setPendingCitations((prev) => [...prev, event.data])
                break
              case 'token':
                contentRef.current += event.data.text
                setStreamingContent(contentRef.current)
                break
              case 'error':
                setStreamError(event.data)
                break
              case 'done':
                setIsStreaming(false)
                break
            }
          },
          onError: (err) => {
            setStreamError(err)
          },
        })
      } finally {
        // 不管这次流是怎么收场的——done 事件、error 事件，还是连接被关闭
        // 但没收到 done——都作废消息列表缓存，重新从后端拉一次。
        //
        // 【为什么失败路径也必须作废】error 帧之后后端其实已经把用户消息
        // 落库了（只把 assistant 那条标成 failed），不重新拉取的话界面就剩
        // 一句"生成失败"，用户刚说过的话在屏幕上凭空消失、在数据库里却存在
        // ——界面和库就此分叉。放在 finally 里是为了覆盖上面三种出口，
        // 不依赖某个分支恰好被走到。
        // 正常结束那条路径本来也要作废（技术方案 §三："页面刷新后从数据库
        // 重载历史与未完成状态"的同一个原则，这里是"流刚结束"这一刻就主动
        // 应用，不等用户手动刷新）。
        setIsStreaming(false)
        queryClient.invalidateQueries({ queryKey: key })
      }
    },
    [conversationId, queryClient],
  )

  return { send, isStreaming, streamingContent, pendingCitations, streamError }
}

export type { Message }
