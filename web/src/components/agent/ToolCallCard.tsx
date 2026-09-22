import { ChevronDown, Loader2, Wrench } from 'lucide-react'
import { useState } from 'react'

import { RUN_STATUS_LABEL, type RunStatus } from '@/components/agent/runStatus'
import { CopyButton } from '@/components/conversation/CopyButton'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

type Props = {
  name: string
  args: unknown
  /** undefined = 还没收到 tool_result。 */
  result?: unknown
  /**
   * 这一次调用的状态；不传就按"有没有收到 tool_result 事件"推（= running /
   * completed 两态）。
   *
   * 【为什么判据可以由外面给】实时视图（AgentDetailPage）手里只有事件，
   * 而事后轨迹页（RunTracePage）读的是数据库里的 Step 行：那一行知道
   * 这次调用是 failed 还是 interrupted（实时视图看不到这两态），也知道
   * 它的 tool_result 列是不是空的——空列序列化出来是 `null`，与"工具
   * 真的返回了 null"在 JSON 里同形，前端分不出来，只有 step.status 能
   * 说清。判据由调用方给，卡片不替它猜。
   */
  stepStatus?: RunStatus
}

/**
 * 展开后先渲染这么多字符，剩下的等用户点「显示全部」。
 *
 * 【为什么要有这个上限】工具结果可能是整篇文档（知识库检索工具就能返回
 * 几万字），一次性铺进 DOM 会让整条时间线变卡，而且用户真正想看的往往
 * 只是开头。数字选在"够看清一个 JSON 结构"与"不拖垮渲染"之间。
 */
const RESULT_PREVIEW_CHARS = 2000

/**
 * 把任意值渲染成可读文本。
 *
 * 【为什么要兜一层】契约里 toolArgs / toolResult 是 unknown，形状由具体
 * 工具决定。JSON.stringify 对 undefined 和函数返回 undefined、对循环引用
 * 和 BigInt 直接抛——抛在这里会让整条轨迹白屏，而那恰恰是最需要看轨迹的
 * 时刻（工具返回了怪东西）。
 */
function toText(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2) ?? String(value)
  } catch {
    return String(value)
  }
}

/**
 * 可折叠的工具调用卡片（issue #80）——前端引用方案 §2.2 点名的交互形态，
 * 插在流式时间线里，默认收起（只显示工具名 + 状态），点开看参数与结果。
 *
 * 【为什么不折叠就渲染不了】工具是 Agent 能力的全部证据：模型选了哪个
 * 工具、传了什么参数、拿回了什么，三者缺一就没法回答"它为什么答错"。
 * 但把三者全铺在时间线上又会让一屏只放得下一次调用——折叠是"默认不打扰、
 * 需要时全都在"的取舍。
 *
 * 【没有用 shadcn 的 Collapsible】这个项目没引入那个组件；一个 useState
 * 足够表达"点开/收起"，不需要 Radix 的无障碍能力（焦点管理、动画状态机）
 * 来配这么小的一块 UI。aria-expanded 自己给上——那是展开控件必须有的语义。
 */
export function ToolCallCard({ name, args, result, stepStatus }: Props) {
  const [expanded, setExpanded] = useState(false)
  const [showFull, setShowFull] = useState(false)

  // 状态字直接用全站那一份六态映射（README §17）——同一件事在徽章上写
  // "运行中"、在卡片上写别的措辞，只会让人以为是两种状态。
  const state: RunStatus = stepStatus ?? (result !== undefined ? 'completed' : 'running')
  const running = state === 'running' || state === 'pending'
  // 「这次调用已经收场」——收场之后才谈得上"结果"这一栏。
  const settled = !running
  const hasResult = settled && result !== undefined

  const resultText = toText(result)
  const clipped = resultText.length > RESULT_PREVIEW_CHARS
  const shownText = clipped && !showFull ? resultText.slice(0, RESULT_PREVIEW_CHARS) : resultText

  return (
    <div className="border-border bg-muted/30 w-fit max-w-full min-w-64 rounded-lg border text-sm">
      {/* 【复制按钮是按钮的兄弟节点，不是子节点】展开控件本身是个 button，
          把复制按钮嵌进去就是 button 套 button——HTML 不允许，浏览器会把
          内层拆出去，DOM 结构和读屏软件看到的顺序都会和写的不一样。 */}
      <div className="flex items-center">
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          className="flex min-w-0 flex-1 items-center gap-2 px-3 py-2 text-left"
        >
          {running ? (
            <Loader2 className="text-muted-foreground size-4 shrink-0 animate-spin" />
          ) : (
            <Wrench className="text-muted-foreground size-4 shrink-0" />
          )}
          <span className="min-w-0 flex-1 truncate font-medium">{name}</span>
          {/* 状态既有旋转图标也有文字（README §21：图标被压停后文字还在，
              "还在跑"这件事才不至于只靠动画表达）。 */}
          <span className="text-muted-foreground shrink-0 text-xs">
            {RUN_STATUS_LABEL[state] ?? state}
          </span>
          <ChevronDown
            className={cn(
              'text-muted-foreground size-4 shrink-0 transition-transform',
              expanded && 'rotate-180',
            )}
          />
        </button>

        {/* 结果还没回来就没有东西可复制。label 带上工具名：一屏可能有好几张
            卡片，读屏软件念一串"复制工具结果"等于没说（README §14）。 */}
        {hasResult && (
          <CopyButton className="mr-2 shrink-0" text={resultText} label={`复制 ${name} 的结果`} />
        )}
      </div>

      {expanded && (
        <div className="border-border space-y-2 border-t px-3 py-2">
          <div>
            <div className="text-muted-foreground mb-1 text-xs">参数</div>
            <pre className="bg-background overflow-x-auto rounded-md p-2 text-xs">{toText(args)}</pre>
          </div>
          {/* 结果还没来就整块不渲染：占位一个空框只会让人以为"结果就是空的"。 */}
          {hasResult && (
            <div>
              <div className="text-muted-foreground mb-1 text-xs">结果</div>
              {/* 保留换行 + 允许在词内断开：结果里常有超长单行（一个 URL、
                  一段没有换行的 JSON），只给横向滚动条的话用户得左右拖着看。 */}
              <pre className="bg-background overflow-x-auto rounded-md p-2 text-xs wrap-break-word whitespace-pre-wrap">
                {shownText}
                {clipped && !showFull && '…'}
              </pre>
              {clipped && !showFull && (
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  className="mt-1"
                  onClick={() => setShowFull(true)}
                >
                  结果共 {resultText.length} 字符，显示全部
                </Button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
