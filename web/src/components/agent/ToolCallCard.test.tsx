// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { ToolCallCard } from '@/components/agent/ToolCallCard'

// 没有配 globals，@testing-library 的自动清理不会生效（它靠全局的 afterEach）。
afterEach(cleanup)

/**
 * 折叠控件本身。
 *
 * 【为什么按 aria-expanded 找，不按名字找】复制按钮的可访问名是
 * 「复制 <工具名> 的结果」，也含工具名——按名字查会同时命中两个按钮。
 * 折叠控件是这里唯一带 aria-expanded 的元素，按它查既准又顺带钉住了
 * "展开控件必须有这个属性"。
 */
function folder() {
  return screen.getByRole('button', { expanded: false })
}

describe('工具调用折叠卡片（issue #80）', () => {
  it('默认收起：只露工具名与状态，参数和结果都不在 DOM 里', () => {
    render(<ToolCallCard name="calculator" args={{ a: 1 }} result={{ answer: 2 }} />)

    expect(screen.getByText('calculator')).toBeTruthy()
    expect(screen.getByText('已完成')).toBeTruthy()
    // 收起态不渲染参数/结果——不然"折叠"就只是视觉上的位移
    expect(screen.queryByText('参数')).toBeNull()
    expect(screen.queryByText('结果')).toBeNull()
    expect(screen.queryByText(/answer/)).toBeNull()
  })

  it('点开后有参数与结果全文，展开控件自己说明当前状态', () => {
    const { container } = render(
      <ToolCallCard name="knowledge_search" args={{ query: '向量检索' }} result={['片段一']} />,
    )

    const toggle = folder()
    // 展开控件必须带 aria-expanded——读屏用户不能靠"参数出现没有"来判断
    expect(toggle.getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(toggle)

    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('参数')).toBeTruthy()
    expect(screen.getByText('结果')).toBeTruthy()
    expect(container.textContent).toContain('向量检索')
    expect(container.textContent).toContain('片段一')
  })

  // 结果还没回来时不能假装它完了，也不能留一个空的结果框——"结果就是空的"
  // 和"还没有结果"是两件事。
  it('还没收到 tool_result 时是运行中，且没有结果栏', () => {
    render(<ToolCallCard name="calculator" args={{ a: 1 }} />)

    expect(screen.getByText('运行中')).toBeTruthy()
    fireEvent.click(folder())
    expect(screen.queryByText('结果')).toBeNull()
    // 没有结果就没有可复制的东西
    expect(screen.queryByRole('button', { name: /复制/ })).toBeNull()
  })

  // 【这条是事后轨迹页的判据】数据库里的 tool_result 是空列时序列化出来是
  // null，与"工具真的返回了 null"同形；只有 step.status 能说清跑完没有。
  it('轨迹页按 step.status 判状态，失败的那一步不会被画成运行中', () => {
    render(
      <ToolCallCard name="calculator" args={{ a: 1 }} result={null} stepStatus="failed" />,
    )

    expect(screen.getByText('失败')).toBeTruthy()
    expect(screen.queryByText('运行中')).toBeNull()
  })

  it('超长结果只渲染前一段，点「显示全部」才铺全文', () => {
    const long = 'x'.repeat(5000)
    const { container } = render(<ToolCallCard name="fetch" args={{}} result={long} />)

    fireEvent.click(folder())

    // 截断：5000 个字符不该整段进 DOM（前 2000 + 一处省略号）
    const pre = container.querySelector('pre')
    expect(pre?.textContent?.length).toBeLessThan(3000)

    // 出口上写明"被截了多少"，用户才知道自己看的是开头还是全部
    const showAll = screen.getByRole('button', { name: /显示全部/ })
    expect(showAll.textContent).toMatch(/共 \d+ 字符/)
    fireEvent.click(showAll)

    expect(container.textContent).toContain(long)
    expect(screen.queryByRole('button', { name: /显示全部/ })).toBeNull()
  })

  it('结果可复制，按钮带对象名（§14）', () => {
    render(<ToolCallCard name="calculator" args={{}} result={{ answer: 2 }} />)

    const copy = screen.getByRole('button', { name: '复制 calculator 的结果' })
    expect(copy).toBeTruthy()
  })

  // 结果是工具给的任意 JSON：循环引用会让 JSON.stringify 抛异常，那不该把
  // 整条轨迹打白——那时候正是最需要看轨迹的时候。
  it('结果里有循环引用也不炸，回退成字符串渲染', () => {
    const circular: Record<string, unknown> = { name: '自引用' }
    circular.self = circular

    render(<ToolCallCard name="weird_tool" args={{}} result={circular} />)
    fireEvent.click(folder())

    expect(screen.getByText('结果')).toBeTruthy()
  })
})
