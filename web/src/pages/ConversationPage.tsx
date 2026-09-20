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

  const { data: history, isPending, error } = useMessages(conversationId)
  const { send, isStreaming, streamingContent, pendingCitations, streamError } =
    useSendMessage(conversationId)

  const [input, setInput] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  // 新消息到达（历史加载完成、流式内容更新）时滚到底部——
  // 聊天界面的基本预期，用户不该需要自己手动滚。
  useEffect(() => {
    scrollRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [history, streamingContent])

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
            history.map((m) => (
              <MessageBubble
                key={m.id}
                role={m.role === 'user' ? 'user' : 'assistant'}
                content={m.content}
              />
            ))
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
