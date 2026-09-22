import { Send, Square } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { MessageActions } from '@/components/conversation/MessageActions'
import { MessageBubble } from '@/components/conversation/MessageBubble'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { useMessages, useSendMessage } from '@/hooks/useMessages'
import { flattenPagesChronologically } from '@/lib/pagination'

/**
 * 聊天页——ChatGPT 式布局（消息区 + 底部输入框），前端引用方案 §2.2。
 *
 * 【没有左侧会话列表】这一轮没有"列出全部会话"的端点（见
 * useConversations.ts 的注释），侧栏因此还是知识库列表，不是会话列表；
 * 这个页面本身只管一个会话内部的消息流。
 */
export default function ConversationPage() {
  const { id } = useParams<{ id: string }>()
  const conversationId = id ?? ''

  const {
    data: historyPages,
    isPending,
    error,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useMessages(conversationId)
  const {
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
  } = useSendMessage(conversationId)

  // 【必须按页倒着摊平】这个列表的分页方向是"第一页给最新的 N 条、翻下一页
  // 拿更早的"，所以 pages[0] 最新、pages[1] 更旧。直接摊平会让更早的消息排在
  // 最新消息的**下面**——整段聊天记录的时间顺序反过来，而且不报错。
  const history = flattenPagesChronologically(historyPages)
  // 「最后一条」的标识：新消息到达时它才变，加载更早的一页时它不变。
  const lastMessageId = history.at(-1)?.id

  const [input, setInput] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  // 【只在真的来了一条新消息时滚到底部】原来依赖的是整个 history 数组，
  // 分页之后加载更早的一页也会让它变——视口会被强行拉到底部，用户刚加载
  // 出来的历史一行都看不到。改成依赖"最后一条消息的 id"。
  //
  // 流式内容每来一个 token 也滚一次（那条气泡在增长，底部要跟着走）。
  useEffect(() => {
    scrollRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [lastMessageId, streamingContent])

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const text = input.trim()
    if (!text || isStreaming) return
    setInput('')
    void send(text)
  }

  /**
   * 这条回答对应的提问——重试 / 重新生成要把同一句话再发一次。
   *
   * 【为什么往前找最近的一条 user 而不是固定取 i-1】契约里的 role 有四种
   * （system / user / assistant / tool），固定按 i-1 取在出现别的角色时
   * 会拿到一条不是提问的消息，然后把它的正文当成新提问发出去——那种错误
   * 不会报错，只是回答对着一段莫名其妙的话。
   *
   * 【找不到就返回 null】第一页是按"最新 N 条"截的，最顶上那一条回答很
   * 可能没有配对的提问；这时两个动作都不渲染（见 MessageActions）。
   */
  const questionFor = (index: number): string | null => {
    for (let i = index - 1; i >= 0; i -= 1) {
      if (history[i].role === 'user') return history[i].content
    }
    return null
  }

  /** 这条消息是不是正在进行的那次重试 / 重新生成的来源。 */
  const activeKindFor = (messageId: string) =>
    activeSource?.messageId === messageId ? activeSource.kind : null

  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col p-6">
      <ScrollArea className="flex-1">
        <div className="space-y-6 pr-4">
          {isPending ? (
            <div className="space-y-4">
              <Skeleton className="h-16 w-2/3 rounded-2xl" />
              <Skeleton className="ml-auto h-10 w-1/2 rounded-2xl" />
            </div>
          ) : error ? (
            <Alert variant="destructive">
              <AlertTitle>加载失败</AlertTitle>
              <AlertDescription>
                <ErrorText error={error} />
              </AlertDescription>
            </Alert>
          ) : (
            <>
              {/* 【「加载更多」在顶部】这个列表是升序的、最新的在底部，所以
                  更早的消息要往上看——按钮放在消息流上方才符合方向直觉。
                  hasNextPage 为假时按钮整个不渲染，而不是禁用它：禁用会让
                  用户以为"再多等一会儿就有了"。 */}
              {hasNextPage && (
                <div className="flex justify-center">
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={isFetchingNextPage}
                    onClick={() => void fetchNextPage()}
                  >
                    {isFetchingNextPage ? '加载中…' : '加载更早的消息'}
                  </Button>
                </div>
              )}
              {history.map((m, i) => (
                <div key={m.id}>
                  <MessageBubble
                    role={m.role === 'user' ? 'user' : 'assistant'}
                    content={m.content}
                  />
                  {/* 操作区只给助手消息。缩进对齐气泡（头像 32px + 间距 12px），
                      不然它会挂在头像下面、看起来像另一条消息的一部分。 */}
                  {m.role === 'assistant' && (
                    <MessageActions
                      className="pl-11"
                      status={m.status}
                      question={questionFor(i)}
                      stopped={stoppedIds.includes(m.id)}
                      regenerated={regeneratedIds.includes(m.id)}
                      active={activeKindFor(m.id)}
                      busy={isStreaming}
                      onRetry={() => {
                        const question = questionFor(i)
                        if (question !== null) void retry(m.id, question)
                      }}
                      onRegenerate={() => {
                        const question = questionFor(i)
                        if (question !== null) void regenerate(m.id, question)
                      }}
                    />
                  )}
                </div>
              ))}
            </>
          )}

          {/* 正在流式生成的这一条不在 history 里（它还没有完整落库,或者
              后端已经落库但列表缓存还没作废）——单独渲染,done 事件到达后
              invalidateQueries 会让它被 history 里的真实数据接管。 */}
          {isStreaming && (
            <MessageBubble
              role="assistant"
              content={streamingContent}
              citations={pendingCitations}
              pending
            />
          )}

          {streamError !== null && (
            <Alert variant="destructive">
              <AlertTitle>生成失败</AlertTitle>
              <AlertDescription>
                <ErrorText error={streamError} />
              </AlertDescription>
            </Alert>
          )}

          <div ref={scrollRef} />
        </div>
      </ScrollArea>

      <form onSubmit={handleSubmit} className="mt-4 flex gap-2">
        <Input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="输入消息…"
          disabled={isStreaming}
          autoFocus
        />
        {/* 【发送与停止是同一个位置上的两个状态】流式进行中把发送换成停止
            （issue #79）——"不能后悔"是流式体验里最贵的一课：BYOK 产品里
            每一个 token 都在花用户自己的额度。用的是同一个按钮位置，用户
            不需要去找第二个按钮。

            【停止是 type="button"】它不是提交：留成 submit 的话，点它会先
            触发一次表单提交（handleSubmit 被 isStreaming 挡住，但那是一次
            白跑的路径，而且语义是错的）。

            【可访问名】两个按钮都只有图标，必须自带名字（README §14）。
            这是审计里记着的缺口，做 #79 时一并补上——不然读屏用户听到的
            是"按钮"，而流式过程中那个位置换了功能。 */}
        {isStreaming ? (
          <Button type="button" variant="secondary" onClick={stop} aria-label="停止生成">
            <Square />
          </Button>
        ) : (
          <Button type="submit" disabled={!input.trim()} aria-label="发送">
            <Send />
          </Button>
        )}
      </form>
    </div>
  )
}
