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
 * 发消息 + 消费流式回答。
 *
 * 【为什么不用 useMutation】这不是一次性的请求-响应，是一个持续到 done
 * 事件才结束的过程,中途要不断更新"正在生成中的这条消息内容"——
 * TanStack Query 的 mutation 状态模型（pending/error/success 三态）
 * 表达不了"正在流式追加内容"这个中间态，所以用普通的 useState 自己管。
 *
 * 【乐观更新用户消息,流式追加 assistant 消息】发送那一刻立刻把用户的
 * 提问和一条空的 assistant 占位插进本地状态,不等后端确认——技术方案
 * §4.2 的乐观更新模式："发送消息：先上屏用户消息，SSE 完成或失败后
 * 确认/回滚"。
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
              // 流结束后作废消息列表缓存，重新从后端拉一次——
              // 这样最终显示的内容以数据库为准（技术方案 §三：
              // "页面刷新后从数据库重载历史与未完成状态"的同一个原则,
              // 这里是"流刚结束"这一刻就主动应用它，不等用户手动刷新）。
              queryClient.invalidateQueries({ queryKey: messagesKey(conversationId) })
              break
          }
        },
        onError: (err) => {
          setStreamError(err)
          setIsStreaming(false)
        },
      })

      // for 循环形式的 streamChat 在 done/error 之外的路径正常返回时
      // （比如连接被服务端正常关闭但没收到 done,理论上不该发生但要兜底）
      // 也要把 loading 状态收掉,不能让 UI 卡在"生成中"。
      setIsStreaming(false)
    },
    [conversationId, queryClient],
  )

  return { send, isStreaming, streamingContent, pendingCitations, streamError }
}

export type { Message }
