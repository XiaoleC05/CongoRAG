import { Check, Copy } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { useErrorToast } from '@/hooks/useErrorToast'
import { writeClipboard } from '@/lib/clipboard'
import { cn } from '@/lib/utils'

type Props = {
  /** 要写进剪贴板的原文。 */
  text: string
  /**
   * 完整的无障碍名，例如「复制回答」「复制 go 代码」。
   *
   * 【必须有对象名】README §14：只有图标的按钮要有可访问名，而且要带上
   * 对象名——一条回答里有好几个「复制」，读屏软件念一串「复制」等于没说。
   *
   * 【为什么是整个名字而不是"对象名"再由这里拼前缀】中英混排的间距
   * 只能由调用方决定：`复制 + go 代码` 要写成「复制 go 代码」（中间有空格），
   * `复制 + 回答` 却要写成「复制回答」（中间不能有空格）。把拼接放在这里，
   * 要么给代码块多出一个"复制go 代码"，要么给回答多出一个"复制 回答"。
   */
  label: string
  className?: string
}

/** 复制后那个对勾停留多久（毫秒）。 */
const COPIED_FEEDBACK_MS = 1500

/**
 * 复制按钮（issue #89）。代码块和整条回答共用。
 *
 * 【为什么用 shadcn 的 Button 而不是自己写个 button】规范要求原语从
 * src/components/ui/ 取。Button 已经带了 focus-visible 的聚焦环
 * （focus-visible:ring-3）、禁用态和图标尺寸档，自己写一遍必然会漏掉
 * 聚焦态——那是 README §14 的硬判据。
 *
 * 【成功为什么也要给反馈】剪贴板在界面之外，不做回执用户没有任何办法
 * 确认。反馈分两层：toast（读屏软件会念）+ 按钮自己换成对勾
 * （README §13："变化就写在用户刚点的那个按钮上"）。对勾会自己变回来，
 * 所以它不承担"唯一证据"的角色，toast 才是。
 */
export function CopyButton({ text, label, className }: Props) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | null>(null)
  const showError = useErrorToast()

  // 消息列表会因为流式追加、分页重排，"已复制"那一秒内按钮可能已经被卸载。
  // 不收掉定时器就是对着卸载后的组件 setState——React 不报错，但属于白做。
  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current)
    },
    [],
  )

  const copy = async () => {
    try {
      await writeClipboard(text)
    } catch (err) {
      // 【为什么走 useErrorToast 而不是自己 toast.error】呈现形式由
      // lib/errors.ts 的 errorPresentation 决定，组件里不许自行判断
      // （README §16）。
      //
      // 它返回 true 表示"调用方还得自己渲染"——对剪贴板这两个 type 不会
      // 发生（认不出的 type 在 mutation 语境下一律是 toast）。留着这半句是
      // 为了"复制失败必须有回执"不被将来改动 errorPresentation 悄悄废掉：
      // 真走到这里，一句笼统的提示也好过什么都不显示。
      if (showError(err)) toast.error('复制失败')
      return
    }

    setCopied(true)
    if (timer.current !== null) window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(false), COPIED_FEEDBACK_MS)
    // 回执文案直接复用无障碍名，两处措辞不会漂
    toast.success(`已${label}`)
  }

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      className={cn('text-muted-foreground hover:text-foreground', className)}
      aria-label={label}
      onClick={() => void copy()}
    >
      {/* 两个图标都留在 DOM 里、用 hidden 切换，是为了让按钮盒子的宽度
          不随图标变化——变宽变窄会把旁边的元素推来推去。 */}
      <Check className={cn('text-primary', !copied && 'hidden')} aria-hidden />
      <Copy className={cn(copied && 'hidden')} aria-hidden />
    </Button>
  )
}
