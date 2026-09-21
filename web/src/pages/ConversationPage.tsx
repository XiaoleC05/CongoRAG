import { Send } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
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
  const { send, isStreaming, streamingContent, pendingCitations, streamError } =
    useSendMessage(conversationId)

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
              {history.map((m) => (
                <MessageBubble
                  key={m.id}
                  role={m.role === 'user' ? 'user' : 'assistant'}
                  content={m.content}
                />
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
        <Button type="submit" disabled={isStreaming || !input.trim()}>
          <Send />
        </Button>
      </form>
    </div>
  )
}
