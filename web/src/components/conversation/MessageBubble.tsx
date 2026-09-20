import { Bot, User } from 'lucide-react'

import { CitationBadge } from '@/components/conversation/CitationBadge'
import type { DisplayCitation } from '@/hooks/useMessages'
import { cn } from '@/lib/utils'

type Props = {
  role: 'user' | 'assistant'
  content: string
  citations?: DisplayCitation[]
  /** 正在流式生成中：内容后面跟一个跳动的光标。 */
  pending?: boolean
}

/**
 * 一条消息气泡。
 *
 * 【引用不是嵌在正文文字里,是紧跟在正文后面的一行角标】真正做到
 * "答案内 [1] [2] 编号"（前端引用方案 §2.2 的字面描述）需要让模型
 * 自己在生成的文字里插入编号标记,这需要专门的 prompt 工程和校验
 * （模型可能编号编错、漏编、对不上实际引用），是比这一轮验证范围更大的
 * 工作。这里退一步：引用作为正文之后的一个"来源"区块整体展示，
 * 仍然是可悬停查看摘要的编号角标（Perplexity 式的核心是"编号 + 悬停
 * 展开来源"，这一点保留了），只是编号出现的位置从"嵌入正文"变成
 * "紧跟正文"。
 */
export function MessageBubble({ role, content, citations, pending }: Props) {
  const isUser = role === 'user'

  return (
    <div className={cn('flex gap-3', isUser && 'flex-row-reverse')}>
      <div
        className={cn(
          'flex size-8 shrink-0 items-center justify-center rounded-full',
          isUser ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground',
        )}
      >
        {isUser ? <User className="size-4" /> : <Bot className="size-4" />}
      </div>

      <div className={cn('min-w-0 max-w-[75%] flex-1', isUser && 'flex flex-col items-end')}>
        <div
          className={cn(
            'rounded-2xl px-4 py-2.5 text-sm whitespace-pre-wrap',
            isUser ? 'bg-primary text-primary-foreground' : 'bg-muted',
          )}
        >
          {content}
          {pending && <span className="ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-current align-text-bottom" />}
        </div>

        {citations && citations.length > 0 && (
          <div className="text-muted-foreground mt-1.5 flex flex-wrap items-center gap-1 text-xs">
            <span>来源：</span>
            {citations.map((c, i) => (
              <CitationBadge key={c.chunkId} index={i + 1} citation={c} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
