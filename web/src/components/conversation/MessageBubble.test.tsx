// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { MessageBubble } from '@/components/conversation/MessageBubble'
import type { DisplayCitation } from '@/hooks/useMessages'

// 没有配 globals，@testing-library 的自动清理不会生效（它靠全局的 afterEach）。
afterEach(cleanup)

const citation: DisplayCitation = {
  chunkId: 'c1',
  documentId: 'd1',
  filename: '手册.md',
  snippet: '片段内容',
  score: 0.87,
}

describe('MessageBubble 的 Markdown 渲染', () => {
  it('助手消息按结构渲染：加粗、列表、代码围栏', () => {
    const { container } = render(
      <MessageBubble
        role="assistant"
        content={'**重点**\n\n- 甲\n- 乙\n\n```ts\nconst a = 1\n```\n'}
      />,
    )

    // 断言挂在 DOM 结构上而不是 getByText：getByText 比的是 textContent，
    // <p> 和它里面的 <strong> 会同时命中，得到"多个元素匹配"。
    expect(container.querySelector('strong')?.textContent).toBe('重点')
    expect(container.querySelectorAll('li')).toHaveLength(2)
    expect(container.querySelector('pre code')?.textContent).toBe('const a = 1\n')
  })

  // 这条是选型的全部理由：正文来自模型，是不可信输入。
  // react-markdown 把 hast 转成 React 元素，不经过 dangerouslySetInnerHTML；
  // 原始 HTML 在 post() 里被换成文本节点，所以看得见、执行不了。
  it('正文里的原始 HTML 变成可见文本，不会被当成标签', () => {
    const raw = '<img src=x onerror="alert(1)">'
    const { container } = render(<MessageBubble role="assistant" content={raw} />)

    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain(raw)
  })

  // 回归面：引用行是正文的兄弟节点，没有"插进正文"这回事。
  // 换 Markdown 渲染器时最容易把它连坐掉，所以钉一条。
  it('引用角标与 Markdown 渲染共存', () => {
    const { container } = render(
      <MessageBubble role="assistant" content={'答案见 **第一条**'} citations={[citation]} />,
    )

    expect(container.querySelector('strong')?.textContent).toBe('第一条')
    // [1] 是 CitationBadge 按 index 渲染的角标
    expect(screen.getByText('[1]')).toBeTruthy()
    expect(screen.getByText('来源：')).toBeTruthy()
  })

  it('用户消息不渲染 Markdown，标记原样显示', () => {
    const { container } = render(<MessageBubble role="user" content={'**重点** 和 `代码`'} />)

    expect(container.querySelector('strong')).toBeNull()
    expect(container.querySelector('code')).toBeNull()
    expect(container.textContent).toContain('**重点**')
  })

  // README §12：一个页面有且只有一个 h1，层级不许跳。模型写出的标题是
  // 答案的排版，不是这一页的大纲——落成 h1-h6 会往会话页里塞进一个 h1，
  // 会话页恰恰是"有意不设标题"的那个例外。
  it('Markdown 标题不落成 h1-h6', () => {
    const { container } = render(<MessageBubble role="assistant" content={'# 结论\n\n正文'} />)

    for (const tag of ['h1', 'h2', 'h3', 'h4', 'h5', 'h6']) {
      expect(container.querySelector(tag)).toBeNull()
    }
    expect(container.textContent).toContain('结论')
  })

  // 流式追加时最刺眼的一种"渲染到一半"是代码围栏只来了一半：如果它先按
  // 段落渲染、等闭合反引号到了再翻成代码块，整条消息会在那一帧重排。
  // CommonMark 规定未闭合的围栏一直吃到文末，所以从敲下围栏那一行起它
  // 就已经是代码块了——这条钉住这个性质（实测，不是照文档抄的）。
  it('只来了一半的代码围栏已经是代码块，不会先渲染成段落再翻牌', () => {
    const { container } = render(
      <MessageBubble role="assistant" content={'说明：\n\n```ts\nconst a = 1\n'} pending />,
    )

    expect(container.querySelector('pre code')?.textContent).toBe('const a = 1\n')
  })

  it('代码块带语言标签，且复制按钮有带对象名的可访问名', () => {
    const { container } = render(
      <MessageBubble role="assistant" content={'```go\npackage main\n```\n'} />,
    )

    expect(container.textContent).toContain('go')
    // README §14：只有图标的按钮带上对象名，读屏软件念一堆"复制"等于没说
    expect(screen.getByRole('button', { name: '复制 go 代码' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '复制回答' })).toBeTruthy()
  })

  // rehype-highlight 对不认识的语言只记一条 vfile message 就返回（读过源码）。
  // 【注意它仍然会给 code 加上 hljs 类名】那行 unshift 在 try 之前执行，
  // 所以判据不能是"有没有 hljs 类"，而是"有没有 token 的 span"——
  // 没有 span 就没有任何 hljs-* 类名，也就没有任何着色。
  it('语言未知时不报错，回退成无高亮的等宽块', () => {
    const { container } = render(
      <MessageBubble role="assistant" content={'```没这个语言\nsome text\n```\n'} />,
    )

    const code = container.querySelector('pre code')
    expect(code?.textContent).toContain('some text')
    expect(code?.querySelector('span')).toBeNull()
    // 语言名照常显示在头部，用户知道这里标了一个不认识的语言
    expect(container.textContent).toContain('没这个语言')
  })

  it('整条回答有复制入口；流式生成中不显示（答案还没定稿）', () => {
    const { unmount } = render(<MessageBubble role="assistant" content="答案" />)
    expect(screen.getByRole('button', { name: '复制回答' })).toBeTruthy()
    unmount()

    render(<MessageBubble role="assistant" content="答案" pending />)
    expect(screen.queryByRole('button', { name: '复制回答' })).toBeNull()
  })

  it('用户消息没有整条复制入口', () => {
    render(<MessageBubble role="user" content="提问" />)
    expect(screen.queryByRole('button', { name: '复制回答' })).toBeNull()
  })
})
