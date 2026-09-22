import { Bot, User } from 'lucide-react'
import { memo } from 'react'

import { CitationBadge } from '@/components/conversation/CitationBadge'
import { CopyButton } from '@/components/conversation/CopyButton'
import { MarkdownContent } from '@/components/conversation/MarkdownContent'
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
 * 【为什么只有助手消息渲染 Markdown（issue #87）】用户消息保持纯文本 +
 * whitespace-pre-wrap，理由是三条：
 *   1. 用户输入的是他自己的话，不是一份文档。他敲 `**重点**` 或 `- 一行`
 *      时多半是真的想打这些字符（比如要求"用 **粗体** 标出关键词"），
 *      把他的话重新当标记解释一遍等于替他改写提问。
 *   2. 这条 issue 的动机只对模型输出成立——模型返回的本来就是 Markdown，
 *      用户敲的不是。
 *   3. pre-wrap 保留了他敲的换行。走 Markdown 之后连续换行会被折成一个
 *      段落，多行排版的提问会当场变形。
 *   代价是用户贴进来的代码块没有等宽字体和高亮。这一条记在这里，
 *   不顺手做——它要的是"用户消息里识别代码围栏"，那是另一件事。
 *
 * 【引用不是嵌在正文文字里，是紧跟在正文后面的一行角标】真正做到
 * "答案内 [1] [2] 编号"（前端引用方案 §2.2 的字面描述）需要让模型
 * 自己在生成的文字里插入编号标记，这需要专门的 prompt 工程和校验
 * （模型可能编号编错、漏编、对不上实际引用），是比这一轮验证范围更大的
 * 工作。这里退一步：引用作为正文之后的一个"来源"区块整体展示，
 * 仍然是可悬停查看摘要的编号角标（Perplexity 式的核心是"编号 + 悬停
 * 展开来源"，这一点保留了），只是编号出现的位置从"嵌入正文"变成
 * "紧跟正文"。
 *
 * 【换成 Markdown 之后这一块没有动】正文容器换了实现，引用行仍然是它的
 * 兄弟节点，CitationBadge 拿到的东西一个字节都没变——它从来没被"插进正文"，
 * 所以不存在"Markdown 解析器把 [1] 吃掉"的问题。两件事的共存由
 * MessageBubble.test.tsx 钉住（同一屏上既渲染出 Markdown 的 strong，
 * 又渲染出引用角标）。
 */
function MessageBubbleImpl({ role, content, citations, pending }: Props) {
  const isUser = role === 'user'

  return (
    <div className={cn('group/message flex gap-3', isUser && 'flex-row-reverse')}>
      <div
        className={cn(
          'flex size-8 shrink-0 items-center justify-center rounded-full',
          isUser ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground',
        )}
      >
        {isUser ? <User className="size-4" /> : <Bot className="size-4" />}
      </div>

      {/* 【窄屏放宽到 85%（issue #86）】75% 是按桌面宽度定的：390px 上气泡只剩
          约 250px，中文每行十来个字就折，一段答案要占好几屏。窄屏把上限放宽到
          85%，sm: 以上回到 75%——宽屏上留着那 25% 是为了让左右两侧的发言
          一眼分得开，这个理由在窄屏上让位给可读性。 */}
      <div
        className={cn('min-w-0 max-w-[85%] flex-1 sm:max-w-[75%]', isUser && 'flex flex-col items-end')}
      >
        <div
          className={cn(
            'rounded-2xl px-4 py-2.5 text-sm',
            isUser ? 'bg-primary text-primary-foreground whitespace-pre-wrap' : 'bg-muted',
          )}
        >
          {isUser ? content : <MarkdownContent content={content} />}
          {/* 流式光标。它现在是正文容器的兄弟节点而不是行内尾巴——Markdown
              渲染出来的最后一块通常是块级元素，行内元素接在它后面必然另起
              一行。换来的是这个方块本身完全不受 Markdown 影响：它在
              index.css 的 prefers-reduced-motion 里有一条专门的兜底
              （压停后必须停在实心块上，见 §21），保持原样就还在。 */}
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

        {/* 整条回答的复制入口（issue #89）。工具结果的复制属后续批次，
            这里不碰 components/agent/。

            【为什么是复制原文而不是渲染后的文字】用户拿走这段内容是要去
            别处用（贴进文档、发给同事）。原文里的列表和加粗是他要的结构，
            转成纯文本就全丢了。

            【为什么流式过程中不显示】这时答案是半截的，复制出去只有害处。
            等 done 事件到达、这条消息换成 history 里的真实数据，按钮自然
            出现。 */}
        {!isUser && !pending && (
          <div
            className={cn(
              'mt-1 flex items-center gap-0.5 transition-opacity',
              // 【触屏没有悬停】README §15：鼠标能悬停看到的东西，触屏上要
              // 有等价物。这里靠两条，缺一不可：
              //   1. sm: 以下（窄屏）一律显示，不参与悬停显隐；
              //   2. 即便在宽屏上，Tailwind 也会把 group-hover 包进
              //      `@media (hover: hover)`——在编译产物里核过。所以一台
              //      平板即使宽度够了，只要它是"没有悬停"的设备，规则就
              //      不生效，操作区自动回到常显。
              //
              // 【为什么是 focus-within 而不是 focus】键盘用户 Tab 进来时
              // 焦点落在按钮上，必须是它自己把操作区点亮——焦点在父节点上
              // 的情况（focus）覆盖不到。只写 focus 的话，用户是在点一个
              // 看不见的按钮。
              'opacity-100 sm:opacity-0 sm:group-hover/message:opacity-100 sm:group-focus-within/message:opacity-100',
            )}
          >
            <CopyButton text={content} label="复制回答" />
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * 【为什么要 memo】流式回答每来一个 token，ConversationPage 就重渲染一次，
 * 历史上每一条消息也会跟着重渲染——而每条消息里都挂着一次 Markdown 解析。
 * 实测 5.6 KB 的答案单次解析+建树约 10.5 ms，几十条历史叠起来足以吃掉
 * 整帧预算。memo 之后只有 content 真的变了的那一条会重算；
 * 历史条目的 content / citations 都是稳定引用，一帧都不会重算。
 */
export const MessageBubble = memo(MessageBubbleImpl)
