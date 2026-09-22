import type { ComponentProps } from 'react'
import { memo } from 'react'
import Markdown from 'react-markdown'
import type { Components, ExtraProps } from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import remarkGfm from 'remark-gfm'

import { CodeBlock } from '@/components/conversation/CodeBlock'
import { cn } from '@/lib/utils'

/**
 * 助手消息正文的 Markdown 渲染（issue #87）。
 *
 * ── 选型：react-markdown + remark-gfm + rehype-highlight ─────────────
 *
 * 【为什么不自己解析 / 不用 innerHTML】raw HTML 那一派（marked、markdown-it
 * 直出字符串 + dangerouslySetInnerHTML）要先"渲染成 HTML 再插进 DOM"，
 * 那就必须再配一个 sanitizer，而且 sanitizer 漏一条就是 XSS。这条路直接排除。
 *
 * 【react-markdown 为什么没有 HTML 注入面】它把 Markdown 解析成 hast，
 * 再由 hast-util-to-jsx-runtime 生成 **React 元素树**，全程不碰
 * dangerouslySetInnerHTML——在 node_modules 里 grep 过：
 * react-markdown/lib/index.js 与 hast-util-to-jsx-runtime/lib/index.js 里
 * 一处都没有。
 *
 * 【模型输出的原始 HTML 会怎样】remark-rehype 默认开着 allowDangerousHtml，
 * 原始 HTML 会以 raw 节点活下来；react-markdown 的 post() 再把它换成
 * **文本节点**（lib/index.js 里 `parent.children[index] = {type: 'text', ...}`）。
 * 实测：喂进去 `<div onclick="alert(1)">x</div>`，产出的是转义后的
 * `&lt;div onclick="alert(1)"&gt;x&lt;/div&gt;`——看得见、执行不了。
 * 这就是这里不加 rehype-raw 的原因：那一个插件的效果正是把这条路重新打开，
 * 之后就得靠 rehype-sanitize 兜底，等于自己造一个新的风险面。
 *
 * 【链接】react-markdown 默认的 urlTransform 会把 `javascript:` 之类的协议
 * 过滤掉，所以 `[点我](javascript:alert(1))` 不会变成一个可执行的链接。
 *
 * 【为什么不设 skipHtml】设了的话原始 HTML 会被整段丢掉。对话里"内容凭空
 * 少了一段"比"看见一段尖括号"更难排查，所以保持默认（转成可见文本）。
 *
 * 【依赖的版本依据】都是读 node_modules 里的 .d.ts 确认的，不是照文档写的：
 *   react-markdown@10.1.0  peerDependencies 是 react >=18（React 19 在范围内），
 *                          Options 里已无 inline / passNode 这些旧字段；
 *   remark-gfm@4.0.1       unified 生态的标准插件，只加语法不加 HTML 面；
 *   rehype-highlight@7.0.2 依赖 lowlight@3，同步执行（不需要 async 插件，
 *                          所以能用同步的 Markdown 组件而不是 MarkdownAsync）。
 */

const REMARK_PLUGINS = [remarkGfm]
const REHYPE_PLUGINS = [rehypeHighlight]

type HeadingProps = ComponentProps<'h1'> & ExtraProps

/**
 * Markdown 的标题不落成 h1–h6。
 *
 * 【为什么】README §12 是硬判据：一个页面有且只有一个 h1，且层级不许跳
 * （对话页恰恰是"有意不设标题"的那个例外）。模型写出的 `# 结论` 是答案的
 * 排版，不是这一页的大纲——落成真标签就会往会话页里塞一个 h1，或者从
 * `###` 起步制造断层，靠标题在页面里跳转的人会直接跳进模型编的树里。
 * 视觉上的粗体与大字号照旧，丢的只是语义层级，而那个层级本来就不属于
 * 这份文档。
 */
function MarkdownHeading({ children, node: _node, className, ...rest }: HeadingProps) {
  return (
    <div {...rest} className={cn('mt-4 font-semibold first:mt-0', className)}>
      {children}
    </div>
  )
}

/**
 * 覆盖表放在模块作用域：每次渲染新建一个对象会让 react-markdown 认为
 * 组件类型变了，整棵子树重挂——流式下每一帧都重挂一次。
 */
const COMPONENTS: Components = {
  pre: CodeBlock,
  h1: MarkdownHeading,
  h2: MarkdownHeading,
  h3: MarkdownHeading,
  h4: MarkdownHeading,
  h5: MarkdownHeading,
  h6: MarkdownHeading,
}

type Props = {
  content: string
  className?: string
}

/**
 * 【为什么 memo】流式回答每来一个 token 就重渲染一次会话页。没有 memo 的话，
 * 屏幕上每一条历史消息都要重新解析一遍 Markdown 并重建元素树——实测一个
 * 5.6 KB 的答案单次解析+生成元素树约 10.5 ms（见交付说明），几十条历史
 * 消息叠起来就是每帧几百毫秒。memo 之后只有"正在增长的那一条"会重算。
 */
export const MarkdownContent = memo(function MarkdownContent({ content, className }: Props) {
  return (
    <div className={cn('chat-markdown', className)}>
      <Markdown
        remarkPlugins={REMARK_PLUGINS}
        rehypePlugins={REHYPE_PLUGINS}
        components={COMPONENTS}
      >
        {content}
      </Markdown>
    </div>
  )
})
