import type { InfiniteData } from '@tanstack/react-query'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useRef, useState } from 'react'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { flattenPages, nextPageParam, type Page } from '@/lib/pagination'
import { streamChat } from '@/lib/streamChat'
import { newIdempotencyKey } from '@/lib/uuid'

type Message = Schemas['Message']

export const messagesKey = (conversationId: string) => ['messages', conversationId]

/**
 * 读：一个会话的历史消息（分页，issue #45）。
 *
 * 【第一页是最新的 N 条，不是最旧的】聊天页打开就该看到最近发生的事；
 * 给最旧的 50 条意味着一个几千条的会话要翻几十次才到最新——那是把分页
 * 做成了功能退化。翻下一页拿的是**更早**的消息。
 *
 * 返回值按 sequence_no 升序（服务端已经排好），页面直接按顺序渲染。
 */
export function useMessages(conversationId: string) {
  return useInfiniteQuery({
    queryKey: messagesKey(conversationId),
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET('/api/v1/conversations/{id}/messages', {
        params: { path: { id: conversationId }, query: { cursor: pageParam ?? undefined } },
      })
      if (error) throw error
      return data
    },
    getNextPageParam: nextPageParam,
  })
}

/**
 * 所有已加载消息里最大的 sequence_no；一条都没有时返回 0。
 *
 * 【为什么不能只看最后一页】页是按「越往后越旧」加载的，但用户可能先翻到
 * 更早再回来发消息——只看最后一页会算出比现有消息更小的序号。
 */
function lastSequenceNo(data: InfiniteData<Page<Message>> | undefined): number {
  let max = 0
  for (const msg of flattenPages(data)) {
    if (msg.sequenceNo > max) max = msg.sequenceNo
  }
  return max
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

      // 【一次用户意图 = 一个键】新按下发送就该是一个新键；将来若加自动
      // 重试，必须把同一个 attempt 的键存在 ref 里复用——每次重试都换新键
      // 等于幂等完全失效，而用户遇到的正是"重试之后多出一轮回答"。
      const idempotencyKey = newIdempotencyKey()

      // 乐观插入：内容、顺序都取自用户刚敲的这一下，不等后端返回 uuid 和
      // sequence_no。随后的作废重取会用数据库里的那一行把它替换掉。
      //
      // 【分页之后要落到最后一页，不是顶层数组】缓存形状是
      // {pages, pageParams}，而"最后一条"是**所有已加载页**里的最后一条
      // （不是第一页的最后一条，也不是 pages 数组的最后一项）。
      queryClient.setQueryData<InfiniteData<Page<Message>>>(key, (old) => {
        const optimistic: Message = {
          id: nextOptimisticId(),
          conversationId,
          role: 'user',
          content: text,
          status: 'completed',
          sequenceNo: lastSequenceNo(old) + 1,
          createdAt: new Date().toISOString(),
        }
        if (!old || old.pages.length === 0) {
          // 还没有任何一页（首屏还在加载）：造一个只含这条乐观消息的页，
          // 否则用户会看到自己刚敲的字什么都没发生。
          return { pages: [{ items: [optimistic] }], pageParams: [null] }
        }
        const pages = [...old.pages]
        const last = pages[pages.length - 1]
        pages[pages.length - 1] = { ...last, items: [...last.items, optimistic] }
        return { ...old, pages }
      })

      try {
        await streamChat(conversationId, text, idempotencyKey, {
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
