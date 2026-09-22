// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { CitationBadge } from '@/components/conversation/CitationBadge'
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

/**
 * 引用角标的展开（issue #86）。
 *
 * 【为什么这里要单开一个文件】改的是 CitationBadge 自己的交互（悬停之外补上
 * 点击、触发器从 `<sup>` 换成真按钮），而它唯一的调用方 MessageBubble 的用例
 * 在讲 Markdown 与引用的共存。把交互断言挂在调用方那边，改这个组件的人不会
 * 去看那个文件。
 *
 * 【它防的是什么】触屏上没有 hover 这件事：只有 HoverCard 的话，手指点不动
 * 角标、来源卡片永远打不开（README §15）。这条用例用 click 走的就是手指的
 * 那条路——jsdom 里没有真的触屏，但点击是两种输入共用的终点。
 */
describe('引用角标的展开', () => {
  it('触发器是真按钮，点一下展开来源卡片、再点一下收起', async () => {
    render(<CitationBadge index={1} citation={citation} />)

    // §14：交互元素必须是真控件。按角色查等于同时钉住了"它是 button、
    // 而且可访问名是 [1]"——换成可点击的 sup / span 这条会直接红。
    const trigger = screen.getByRole('button', { name: '[1]' })

    // 一开始不展开：卡片里的文件名不该在页面上
    expect(screen.queryByText(citation.filename)).toBeNull()

    fireEvent.click(trigger)
    expect(await screen.findByText(citation.filename)).toBeTruthy()
    // 分数与片段也在卡片里——这是"来源卡片"和"一句 tooltip"的区别
    expect(screen.getByText('相似度 87%')).toBeTruthy()

    fireEvent.click(trigger)
    await waitFor(() => expect(screen.queryByText(citation.filename)).toBeNull())
  })

  it('悬停仍然能展开：点击那条不是把原来的用法换掉', async () => {
    render(<CitationBadge index={2} citation={citation} />)

    fireEvent.pointerEnter(screen.getByRole('button', { name: '[2]' }))

    // Radix 的 HoverCard 有 openDelay（这里 100ms），所以是等它一下，不是立即断言
    expect(await screen.findByText(citation.filename)).toBeTruthy()
  })
})
