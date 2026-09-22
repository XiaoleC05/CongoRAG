import { Loader2, RefreshCw, RotateCcw } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { Message } from '@/hooks/useMessages'
import { cn } from '@/lib/utils'

type Props = {
  /** 这条回答的状态，决定给的是「重试」还是「重新生成」。 */
  status: Message['status']
  /**
   * 这条回答对应的提问。找不到时（第一页正好截在半轮对话之间）没有可重发的
   * 正文，两个动作都不渲染——渲染一个点了没反应的按钮比不渲染更糟。
   */
  question: string | null
  /** 这一条是被用户按「停止」停下来的那次生成。 */
  stopped?: boolean
  /** 这一条已经被重新生成取代（旧回答保留为一条分支）。 */
  regenerated?: boolean
  /** 这一条正是正在进行的那次重试/重新生成——按钮位置改显示进行中。 */
  active?: 'retry' | 'regenerate' | null
  /** 会话里已经有流在跑：所有动作都不许再点（README §16 防重复提交）。 */
  busy?: boolean
  onRetry: () => void
  onRegenerate: () => void
  className?: string
}

/**
 * 一条助手消息下面的操作区（issue #90）。
 *
 * 【为什么不在 MessageBubble 里】气泡负责"这段话长什么样"，操作区负责
 * "拿它还能做什么"。分开之后引用、Markdown、复制各自的位置不用互相让位，
 * 而且气泡里那几个按钮的悬停显隐规则不必再考虑多一行。
 *
 * 【和停止按钮是同一片交互区】停止、重试、重新生成是同一组操作（issue #90
 * 的原话）：用户按下停止之后最可能的下一步就是重试，所以「已停止生成」和
 * 「重试」出现在同一行，中间不隔任何东西。
 *
 * 【三种状态怎么表达】失败的那一轮给「重试」，成功的回答给「重新生成」，
 * 被停止的那一轮也归在失败侧（它在库里的终态就是 failed）。这不是前端
 * 自己发明状态机——status 就是契约里那个枚举，只是把 failed 拆成两种出处
 * 不同的展示：用户自己停的，和真的出错的。这个区分只能由前端记着
 * （stoppedIds），因为普通聊天那一侧两种都写 failed。
 *
 * 【按钮为什么不写 aria-label】它们带着可见文字，文字本身就是可访问名。
 * 再补一个 aria-label 只会让"可见文字"和"念出来的名字"分叉，语音控制
 * 用户说"点击重试"就点不动了（WCAG 2.5.3）。只有图标按钮才需要它
 * （README §14）。
 */
export function MessageActions({
  status,
  question,
  stopped,
  regenerated,
  active,
  busy,
  onRetry,
  onRegenerate,
  className,
}: Props) {
  const isFailed = status === 'failed' || stopped === true
  const hasAction = question !== null && (isFailed || status === 'completed')

  // 什么都没有就整行不渲染：留一个空的 div 会白白撑出一段间距，而消息之间
  // 的间距是排版语言的一部分。（active 也要算进"有东西"——它是个进行中
  // 指示，藏掉它等于把"正在重试"这件事也一起藏了。）
  if (!hasAction && !stopped && !regenerated && !active) return null

  return (
    <div className={cn('mt-1 flex flex-wrap items-center gap-1', className)}>
      {/* 「已停止生成」是一条持久记录，不是一闪而过的提示（README §17：
          失败要说清是哪一轮，"这一轮"的记录留在页面上）。 */}
      {stopped && (
        <Badge variant="outline" className="mr-1">
          已停止生成
        </Badge>
      )}

      {/* 【旧回答保留为一条分支，不假装它没存在过】后端没有删消息的端点，
          这一轮已经落库、已经被引用、也已经计过费——前端把它藏起来只会让
          界面和数据库分叉。改成明确标出"你看到的是一条旧分支"，用户能对照
          两次采样，也解释得清下面为什么还有一条同样的提问。 */}
      {regenerated && (
        <Badge variant="outline" className="mr-1">
          已被重新生成
        </Badge>
      )}

      {active ? (
        // 【进行中要有 loading 态】按钮已经 disabled，但"我在做什么"必须写在
        // 用户刚点的那个位置上——不然这一轮的重试和下一轮的重试在界面上
        // 长得一模一样。旋转图标旁边有文字（README §21）。
        <span className="text-muted-foreground inline-flex items-center gap-1.5 text-xs">
          <Loader2 className="size-3 animate-spin" />
          {active === 'retry' ? '重试中…' : '重新生成中…'}
        </span>
      ) : (
        question !== null && (
          <>
            {isFailed && (
              <Button type="button" variant="ghost" size="xs" disabled={busy} onClick={onRetry}>
                <RotateCcw aria-hidden />
                重试
              </Button>
            )}

            {status === 'completed' && (
              <Button
                type="button"
                variant="ghost"
                size="xs"
                disabled={busy}
                onClick={onRegenerate}
              >
                <RefreshCw aria-hidden />
                重新生成
              </Button>
            )}
          </>
        )
      )}
    </div>
  )
}
