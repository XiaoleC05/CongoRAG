import { ChevronDown, Loader2, Wrench } from 'lucide-react'
import { useState } from 'react'

import { cn } from '@/lib/utils'

type Props = {
  name: string
  args: unknown
  /** undefined = 还没收到 tool_result,渲染成"运行中"。 */
  result?: unknown
}

/**
 * 可折叠的工具调用卡片——前端引用方案 §2.2 点名的交互形态，插在流式
 * 时间线里,默认收起（只显示工具名 + 状态),点开看完整的参数/结果。
 *
 * 没有用 shadcn 的 Collapsible（这个项目没引入那个组件）——一个纯
 * useState 切换足够表达"点开/收起"这个简单交互,不需要 Radix 的
 * 无障碍能力（焦点管理、动画状态机）来配这么小的一块 UI。
 */
export function ToolCallCard({ name, args, result }: Props) {
  const [expanded, setExpanded] = useState(false)
  const done = result !== undefined

  return (
    <div className="border-border bg-muted/30 w-fit min-w-64 rounded-lg border text-sm">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left"
      >
        {done ? (
          <Wrench className="text-muted-foreground size-4 shrink-0" />
        ) : (
          <Loader2 className="text-muted-foreground size-4 shrink-0 animate-spin" />
        )}
        <span className="flex-1 font-medium">{name}</span>
        <span className="text-muted-foreground text-xs">{done ? '已完成' : '运行中…'}</span>
        <ChevronDown
          className={cn('text-muted-foreground size-4 shrink-0 transition-transform', expanded && 'rotate-180')}
        />
      </button>

      {expanded && (
        <div className="border-border space-y-2 border-t px-3 py-2">
          <div>
            <div className="text-muted-foreground mb-1 text-xs">参数</div>
            <pre className="bg-background overflow-x-auto rounded-md p-2 text-xs">
              {JSON.stringify(args, null, 2)}
            </pre>
          </div>
          {done && (
            <div>
              <div className="text-muted-foreground mb-1 text-xs">结果</div>
              <pre className="bg-background overflow-x-auto rounded-md p-2 text-xs">
                {JSON.stringify(result, null, 2)}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
