import type { InfiniteData } from '@tanstack/react-query'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { flattenPages, flattenPagesChronologically, nextPageParam, type Page } from '@/lib/pagination'
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

/**
 * 已加载消息里最后一条助手消息的 id；没有则返回 null。
 *
 * 【给它一个函数而不是就地写 reverse().find()】发送前后各要取一次（见
 * runTurn 里"被停下来的那条回答"那段），两处写法必须一致——写两遍迟早
 * 有一处忘了按时间正序排，而那个错误的表现是"标记挂在了错误的回答上"。
 */
function lastAssistantId(data: InfiniteData<Page<Message>> | undefined): string | null {
  const chronological = flattenPagesChronologically(data)
  for (let i = chronological.length - 1; i >= 0; i -= 1) {
    if (chronological[i].role === 'assistant') return chronological[i].id
  }
  return null
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
 * 这次重试 / 重新生成是从哪条消息发起的。
 *
 * 【为什么要记 messageId】「重试中…」必须写在用户刚点的那一行上，否则
 * 这一轮的重试和下一轮的重试在界面上长得一模一样。
 */
export type TurnSource = { messageId: string; kind: 'retry' | 'regenerate' }

/**
 * 发消息 + 消费流式回答。停止、重试、重新生成也都从这里出（issue #79/#90）。
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
 *
 * 【重试 / 重新生成走的是同一条 POST】契约里没有"重新生成这一轮"的端点
 * （只有 POST /conversations/{id}/messages），所以两者都是"把同一句提问
 * 再发一次"。这有两个后果，都写在代码里而不是留给用户猜：
 *   1. 旧回答留在会话里 —— 前端标出「已被重新生成」，不假装它没存在过；
 *   2. 后端会为这次重发再写一条用户消息 —— 界面上因此会出现两条同样的
 *      提问，那正是"保留为一条分支"的字面样子。
 */
export function useSendMessage(conversationId: string) {
  const queryClient = useQueryClient()
  const [pendingCitations, setPendingCitations] = useState<DisplayCitation[]>([])
  const [streamingContent, setStreamingContent] = useState('')
  const [isStreaming, setIsStreaming] = useState(false)
  const [streamError, setStreamError] = useState<unknown>(null)
  /**
   * 被用户按「停止」中止的那些回答（messages.id）。
   *
   * 【只活在这一次页面会话里】后端那边它是一条 failed（枚举里没有"中断"
   * 这一档，见 conversation.streamToClient 的注释），刷新之后这个标记就没了，
   * 界面给出的是「重试」——后端没记住的事，前端不假装记住。
   */
  const [stoppedIds, setStoppedIds] = useState<string[]>([])
  /** 已经被重新生成取代的那些回答（messages.id）——旧分支的标记。 */
  const [regeneratedIds, setRegeneratedIds] = useState<string[]>([])
  /** 正在进行的那次重试 / 重新生成。 */
  const [activeSource, setActiveSource] = useState<TurnSource | null>(null)

  // 用 ref 存正在拼接的完整内容——streamingContent 这个 state 用来渲染,
  // 但连续的 setState 调用之间读不到彼此的最新值,拼接内容必须用 ref。
  const contentRef = useRef('')
  // 这一条流的 AbortController。「停止」按的就是它（issue #79）。
  const controllerRef = useRef<AbortController | null>(null)

  // 【离开页面 / 换会话就把这条流掐掉（issue #102）】组件卸载之后 fetch 与
  // ReadableStream 不会跟着结束：服务端会把这一轮跑完，**每一个 token 都在
  // 花用户自己配的额度**，而那个位置上的「停止」按钮已经跟着页面消失了，
  // 用户再也没有取消入口。
  //
  // 【依赖写 conversationId】换会话时在途的那条流属于上一个会话——它同样
  // 该被中止，而且这时候界面上给出的已经是另一个会话了。
  //
  // 【为什么读 ref 而不是把 controller 当依赖】要掐的永远是"当下这条流"，
  // 而 controller 每次 runTurn 都换新的；把它写进依赖会让这个 effect 每发
  // 一轮消息就重跑一次，清理函数在重跑时把刚建好的流误杀。
  useEffect(() => () => controllerRef.current?.abort(), [conversationId])

  const key = messagesKey(conversationId)

  const runTurn = useCallback(
    async (
      text: string,
      options: {
        /** 这是一条新提问（要乐观上屏），还是"同一句再来一次"。 */
        optimisticUserMessage: boolean
        source?: TurnSource
      },
    ) => {
      // 【进行中不许再来一次】按钮已经 disabled 了，但按钮之外还有别的入口
      // （在输入框里回车的那个提交、以及"按下停止的同一帧里又点了重试"），
      // 这一条是最后一道闸：同时跑两条流的话，两份 state 会互相覆盖，
      // 而表现只是"答案看起来串了"。
      if (controllerRef.current) return

      const controller = new AbortController()
      controllerRef.current = controller

      setIsStreaming(true)
      setStreamError(null)
      setStreamingContent('')
      setPendingCitations([])
      contentRef.current = ''
      setActiveSource(options.source ?? null)

      const source = options.source

      // 【一次用户意图 = 一个键】新按下发送就该是一个新键。
      //
      // 【重试与重新生成也必须换一个键——这不是同一次尝试的重传】幂等键
      // 管的是"这一次请求有没有已经执行过"，命中时服务端把那一轮**已记录
      // 的事件**补发一遍。而失败的那一轮记下来的终态就是那条 error 帧：
      // 复用同一个键得到的是一次重放，用户再看到同一个错误，等于什么都没
      // 做。重新生成更是必须真的重新采样一次，否则拿回来的是同一个答案。
      // 所以这里和 send 走同一条路：每次都新键。要用幂等的是"断线之后重发
      // 同一个 attempt"，那个入口在 streamChat 的调用方（现在还只是发送
      // 那一刻的一次性请求），不在这里。
      const idempotencyKey = newIdempotencyKey()

      // 乐观插入：内容、顺序都取自用户刚敲的这一下，不等后端返回 uuid 和
      // sequence_no。随后的作废重取会用数据库里的那一行把它替换掉。
      //
      // 【追加到 pages[0] 的尾部，不是 pages 数组的最后一项】页是按「越往后
      // 越旧」加载的（见 lib/pagination.ts 的 flattenPagesChronologically），
      // 所以 pages[0] 是**最新**的一页、pages[pages.length - 1] 反而是已加载
      // 页里**最旧**的那一页。翻过历史（pages.length >= 2）之后往数组末尾
      // 追加，倒着摊平出来这条提问会落在聊天记录的中间：屏幕上什么都没多
      // 出来，而下面的回答照常一个字一个字地涨。这一页内部是升序的，所以
      // 最新那条就该挂在这一页的尾部。
      if (options.optimisticUserMessage) {
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
          const newest = pages[0]
          pages[0] = { ...newest, items: [...newest.items, optimistic] }
          return { ...old, pages }
        })
      }

      // 发送前记下当时最后一条助手消息——中止之后要靠"它变了没有"来认出
      // 被停下来的那条回答。见下面 finally 里的说明。
      const assistantBefore = lastAssistantId(queryClient.getQueryData(key))

      // 这一轮有没有已经走到终态。判"是不是用户停的"时要一起看它：done 已经
      // 到了、用户的手指正好落在停止按钮上的那一瞬，中止会把一条**已经生成
      // 完**的回答标成"已停止生成"——那是假的，而且这条错标会一直挂在页面上。
      let terminal: 'done' | 'error' | null = null

      try {
        // 【返回值是这次的续传游标，这里不接】重连循环（
        // GET /conversations/{id}/events?after_event_id=）还没做，现在唯一的
        // 消费者是测试——它钉的是"中止与 id 缺席的帧都不推进游标"这条
        // ADR-005 的规则。等重连真的要做时，游标从这里接出去。
        await streamChat(
          conversationId,
          text,
          idempotencyKey,
          {
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
                  terminal = 'error'
                  setStreamError(event.data)
                  break
                case 'done':
                  // 【不在这里收流】`isStreaming` 控制的是"那条流式气泡还在不在"，
                  // 而它一关，气泡就没了、历史里又还没有这条回答。收流放在
                  // finally 的重取之后（见那里的注释）。
                  terminal = 'done'
                  break
              }
            },
            onError: (err) => {
              terminal = 'error'
              setStreamError(err)
            },
          },
          controller.signal,
        )
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
        // 【标记"已被重新生成"要等这次请求真的没出错】写在请求发出之前的话，
        // 一次根本没到服务端的重新生成（后端没起、代理断了）会在界面上同时
        // 留下红色「生成失败」和旧回答下面的「已被重新生成」——两句互相矛盾，
        // 而那一轮从没被重新生成过、也没有任何新分支。
        //
        // 判据是"没有出错且没有被中止"，而不是 terminal === 'done'：流正常
        // 结束不一定收到过 done 帧（连接被静默关掉、测试里的假流），把那种
        // 情况排除掉会让标记在不该缺席的时候缺席。
        if (terminal !== 'error' && !controller.signal.aborted && source?.kind === 'regenerate') {
          setRegeneratedIds((prev) =>
            prev.includes(source.messageId) ? prev : [...prev, source.messageId],
          )
        }

        // 【先作废重取，再放开 UI】反过来的话有一段窗口：`isStreaming` 已经
        // 是 false、流式气泡被卸载，而历史缓存里还没有这条回答（它要等重取
        // 回来）——屏幕上刚生成好的答案会**消失一下**，要等一次往返才回来，
        // 而这段时间输入框已经能打字了。
        controllerRef.current = null
        await queryClient.invalidateQueries({ queryKey: key })
        setIsStreaming(false)
        setActiveSource(null)

        // 【中止之后要认得"被停下来的那条回答"】被中止的那一轮后端会以
        // failed 终态落库，并把已经生成的部分写进 content（failMessage 的
        // 那句"必须把 content 带上"）——重取之后它就是历史里新出现的那条
        // 助手消息。这里把它的 id 记下来，界面才标得出「已停止生成」，
        // 而不是让它看起来像一次普通的失败。
        //
        // 【为什么用重取之后的 id，不拿本地那份 streamingContent 去匹配】
        // 两者是同一条回答的两个副本，重取之后只留下库里那条；按内容匹配会
        // 在"半截内容恰好等于某条历史"时误判。而"id 变了没有"这个判据同时
        // 挡住了另一种误判：用户在首帧到达之前就按了停止（那时后端还没写出
        // 这一轮的助手行），assistantBefore 与重取后的值是同一个，不该标记。
        //
        // 【terminal === null 那一半】这一轮已经收到过 done / error 时，中止
        // 不代表"这一轮被停下来了"——它只说明用户的手指落在了一个即将消失的
        // 按钮上。把已经跑完的回答标成"已停止生成"，是界面在说假话。
        if (controller.signal.aborted && terminal === null) {
          const stopped = lastAssistantId(queryClient.getQueryData(key))
          if (stopped !== null && stopped !== assistantBefore) {
            setStoppedIds((prev) => (prev.includes(stopped) ? prev : [...prev, stopped]))
          }
        }
      }
    },
    [conversationId, key, queryClient],
  )

  const send = useCallback(
    (text: string) => runTurn(text, { optimisticUserMessage: true }),
    [runTurn],
  )

  /**
   * 重试这一轮（issue #90）。text 是那条回答对应的提问。
   *
   * 【重试的是"再生成一次"，不是"把上一次的请求再发一遍"】两者的区别就是
   * 幂等键换不换，见 runTurn 里那段注释。
   */
  const retry = useCallback(
    (failedMessageId: string, text: string) =>
      runTurn(text, {
        optimisticUserMessage: false,
        source: { messageId: failedMessageId, kind: 'retry' },
      }),
    [runTurn],
  )

  /** 重新生成一条已经完成的回答（issue #90）。 */
  const regenerate = useCallback(
    (answerId: string, text: string) =>
      runTurn(text, {
        optimisticUserMessage: false,
        source: { messageId: answerId, kind: 'regenerate' },
      }),
    [runTurn],
  )

  /**
   * 停止生成（issue #79）。
   *
   * 【中止不走错误路径】abort 之后 streamChat 不调 onError（见那边的注释），
   * 所以界面上出现的是「已停止生成」，不是「生成失败」——用户自己按的停止
   * 不该被报成一个错误。
   */
  const stop = useCallback(() => {
    controllerRef.current?.abort()
  }, [])

  return {
    send,
    retry,
    regenerate,
    stop,
    isStreaming,
    streamingContent,
    pendingCitations,
    streamError,
    stoppedIds,
    regeneratedIds,
    activeSource,
  }
}

export type { Message }
