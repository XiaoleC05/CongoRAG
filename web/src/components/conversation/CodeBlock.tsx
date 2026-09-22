import type { ComponentProps } from 'react'
import type { ExtraProps } from 'react-markdown'

import { CopyButton } from '@/components/conversation/CopyButton'
import { cn } from '@/lib/utils'

type Props = ComponentProps<'pre'> & ExtraProps

/**
 * hast 节点的类型从 react-markdown 的 ExtraProps 里取，不直接
 * `import type { Element } from 'hast'`。
 *
 * 【为什么】hast 只是 react-markdown 的传递依赖（它自己依赖的是
 * @types/hast，而且装在 pnpm 的虚拟目录里），web/ 这一层的 node_modules
 * 里根本没有这个名字，直接 import 会得到 TS2307: Cannot find module 'hast'。
 * 从 ExtraProps 里取等于让 react-markdown 告诉我们它塞进来的到底是什么，
 * 也顺带不会和它升级后的类型漂移。
 */
type HastNode = NonNullable<ExtraProps['node']>
type HastChild = HastNode['children'][number]

/** 把 hast 子树里的文字拼回来。只有 text 节点带内容，其余递归下去。 */
function hastText(node: HastChild): string {
  if (node.type === 'text') return node.value
  if (node.type === 'element') return node.children.map(hastText).join('')
  return ''
}

/**
 * 取代码块的语言。
 *
 * 【类名是什么形态】remark-rehype 把围栏上的语言写成 `<code class="language-ts">`；
 * MarkdownContent 里的 highlightPlugin 认得的话再往前塞一个 `hljs`（实测产物：
 * `class="hljs language-ts"`）。所以不能只看第一个类名，要按前缀找。
 * 语言没写、或者不在 MarkdownContent 的 HIGHLIGHT_GRAMMARS 子集里时，
 * 这里返回 undefined——上层照常渲染成无高亮的等宽块，不报错（插件对不在
 * 子集里的语言保留原文返回，那条降级路径有 MarkdownContent.test.tsx 钉着）。
 */
function languageOf(node: HastNode | undefined): string | undefined {
  const code = node?.children[0]
  if (!code || code.type !== 'element') return undefined

  const classes = code.properties.className
  if (!Array.isArray(classes)) return undefined

  for (const value of classes) {
    const name = String(value)
    if (name.startsWith('language-')) return name.slice('language-'.length)
  }
  return undefined
}

/**
 * 代码块（issue #88）。在 react-markdown 的 `pre` 位置覆盖。
 *
 * 【为什么覆盖 pre 而不是 code】react-markdown v10 对"行内的 `x`"和
 * "围栏代码块"都会调 code 组件，而 v10 去掉了 inline 参数（读
 * node_modules/react-markdown/lib/index.d.ts 确认过，Options 里没有它）。
 * 靠 className 有没有 language- 判断，会把"没写语言的围栏块"误判成行内。
 * pre 只可能来自代码块，判据是干净的。
 *
 * 【高亮不在这里做】token 的颜色来自 MarkdownContent 的 highlightPlugin 在构建
 * hast 时插进去的 hljs-* 类名，映射表在 src/index.css。组件里因此不出现任何色值——
 * README §9 的零硬编码色值判据对这一块成立。
 *
 * 【外壳：语言标签 + 复制按钮 + 横向滚动】
 *   - 头部一直显示（不做悬停才出现）：触屏没有悬停，README §15 要求
 *     悬停能看到的东西在触屏上有等价物。代码块的复制是刚需操作，
 *     做成"要点一下才出现"只是把它藏起来。
 *   - `overflow-x-auto` 在 pre 上。没有它，一行长代码会把气泡和整页撑宽
 *     （窄屏的完整响应式策略是 issue #86，但代码块不撑破布局是底线）。
 *
 * 【背景为什么是 bg-background 而不是 bg-muted】气泡本身就是 bg-muted，
 * 代码块再用一层同色等于没有底色，只剩边框——那看起来像"这里还没画完"。
 * 用 bg-background 和已有的 ToolCallCard 里那段 `<pre>` 是同一个做法
 * （它在 bg-muted/30 的卡片里也用 bg-background），深浅两个主题下都能
 * 和气泡分开。
 */
export function CodeBlock({ node, children, className, ...rest }: Props) {
  const lang = languageOf(node)
  const text = node ? hastText(node) : ''

  return (
    <div className="border-border bg-background overflow-hidden rounded-lg border">
      <div className="border-border flex items-center gap-2 border-b px-2 py-1">
        {/* 语言名是给眼睛看的，不是必须的信息；没有语言时留空，
            但仍然占住左侧，让复制按钮稳定地靠右。 */}
        <span className="text-muted-foreground pl-1 font-mono text-xs">{lang ?? ''}</span>
        <CopyButton text={text} label={lang ? `复制 ${lang} 代码` : '复制代码'} className="ml-auto" />
      </div>

      <pre
        {...rest}
        className={cn('overflow-x-auto p-3 font-mono text-xs leading-relaxed', className)}
      >
        {children}
      </pre>
    </div>
  )
}
