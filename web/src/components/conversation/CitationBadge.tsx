import { FileText } from 'lucide-react'
import { useState } from 'react'

import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from '@/components/ui/hover-card'
import type { DisplayCitation } from '@/hooks/useMessages'

/**
 * Perplexity 式引用角标：正文里的 [1] [2]，悬停或点击展开来源卡片。
 * 前端引用方案 §2.2 点名的交互——citation 是一等公民,随流下发,
 * 不是等答案生成完才给（这个属性由后端保证,这里只负责展示）。
 *
 * 【引用只在流式那条气泡上存在】契约里消息没有 citations 字段，引用不落库：
 * 这一轮的角标只在生成过程中看得见，流一结束、气泡换成落库的那条，它就没有了。
 * 所以"某个会话里存着一堆引用角标"是不存在的，验证它必须趁流还在跑。
 *
 * 【为什么把展开状态收到自己手里（issue #86）】原来只有 HoverCard 一条路：
 * 鼠标能悬停，手指没有等价交互（README §15：鼠标能悬停看到的东西，触屏上
 * 要能点开或长按）。这里的做法是**受控**：`open` / `onOpenChange` 交给
 * Radix，同时给触发器加一个点击切换。
 *
 * 【为什么不是直接换成 Popover】Radix 的 HoverCard 在**聚焦时也会开**
 * （键盘 Tab 到触发器就展开，所以它本来就是键盘可达的），换成 Popover
 * 会把这个白拿的能力丢掉，桌面上最常见的"扫一眼来源"也变成要点一下。
 * 两条输入写同一个状态，比二选一更省。
 *
 * 【悬停离开会关掉点击打开的那一次】这是有意的：鼠标用户点开之后再移开
 * 就应该收起，否则卡片会一直挂在正文上挡住下面的话。触屏上不存在
 * pointerleave，所以点开之后保持展开，点同一个角标再收起来。
 */
export function CitationBadge({ index, citation }: { index: number; citation: DisplayCitation }) {
  const [open, setOpen] = useState(false)

  return (
    <HoverCard open={open} onOpenChange={setOpen} openDelay={100}>
      <HoverCardTrigger asChild>
        {/* 【为什么外面还要留一层 sup】上标是排版（sup 的 font-size: smaller
            与 vertical-align: super），控件是语义，两件事压在同一个元素上
            必然丢掉一边。所以 sup 只负责"这是个上标"，里面那个 button 才是
            真控件（§14：交互元素必须是 button / a / Radix 组件）。
            【为什么是 button 而不是 span + role】不需要自己补 role /
            tabIndex / onKeyDown——原生 button 自带键盘与焦点语义，
            而这里要的就是"可聚焦、可点击"这一件事。

            【leading-none 与那圈 padding 是给命中区的，不是给排版的】
            Tailwind 的 preflight 把 sup 写成 `line-height: 0`（它要为"上标
            不影响行高"负责），于是角标的盒子高度实测是 0px——旧版那个 sup
            也一样。0 高的盒子本身没有可命中的面积，能点中靠的是文字的字形盒
            （实测：坐标点在那一点上，命中的是 sup 的文字）。字形盒只有 9px
            高，做触屏目标太小：这里给 sup 一个 line-height: 1、给按钮前后
            2px、上下 6px 的 padding，命中区变成 21px 高。负 margin 把这圈
            padding 从行内占位里扣回去，所以文字与相邻角标的位置一个像素都没动
            （角标之间隔着 gap-1 的 4px，两个方向都不会重叠）。
            【实测】`document.elementFromPoint(角标中心)` 在新版返回的是这个
            button 自己；390px 的触屏上点一下开、再点一下关。 */}
        <sup className="leading-none">
          <button
            type="button"
            // 与悬停共用同一个 open：点一下展开，再点一下收起。
            onClick={() => setOpen((v) => !v)}
            className="text-primary focus-visible:ring-ring/50 -mx-0.5 -my-1.5 cursor-pointer rounded-sm px-0.5 py-1.5 font-medium outline-none hover:underline focus-visible:ring-2"
          >
            [{index}]
          </button>
        </sup>
      </HoverCardTrigger>
      <HoverCardContent className="w-80" side="top">
        <div className="flex items-start gap-2">
          <FileText className="text-muted-foreground mt-0.5 size-4 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">{citation.filename}</div>
            <p className="text-muted-foreground mt-1 line-clamp-4 text-xs whitespace-pre-wrap">
              {citation.snippet}
            </p>
            <div className="text-muted-foreground mt-1.5 text-xs">
              相似度 {(citation.score * 100).toFixed(0)}%
            </div>
          </div>
        </div>
      </HoverCardContent>
    </HoverCard>
  )
}
