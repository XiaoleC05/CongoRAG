// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { MemoryRouter, useLocation } from 'react-router'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import { api } from '@congorag/api-client'
import { CommandPalette } from '@/components/CommandPalette'

/**
 * 命令面板的交互面：快捷键、过滤、键盘选择、导航、关闭与焦点归还。
 *
 * 【为什么要 mock 整个 api-client】面板的数据源是三个列表 hook；用例要验的是
 * "这些数据怎么变成可搜索的条目"，不是 HTTP。mock 掉网络层之后，每个用例
 * 只关心"输入什么、选中什么、导航到哪"。
 */
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn() } }))

const getMock = api.GET as unknown as Mock

const KB = {
  id: '11111111-1111-4111-8111-111111111111',
  name: 'Transformer 论文库',
  createdAt: '2026-09-01T00:00:00Z',
  updatedAt: '2026-09-01T00:00:00Z',
}
const CONVERSATION = {
  id: '22222222-2222-4222-8222-222222222222',
  title: '关于注意力机制的讨论',
  createdAt: '2026-09-02T00:00:00Z',
  updatedAt: '2026-09-02T00:00:00Z',
}
const AGENT = {
  id: '33333333-3333-4333-8333-333333333333',
  name: '算数助手',
  description: '',
  instruction: '',
  toolNames: ['calculator'],
  createdAt: '2026-09-03T00:00:00Z',
  updatedAt: '2026-09-03T00:00:00Z',
}

/** 三个列表端点都返回一条数据；其余路径直接炸（防止用例漏 mock 后静默失败）。 */
function mockLists() {
  getMock.mockImplementation((path: string) => {
    if (path === '/api/v1/knowledge-bases') return Promise.resolve({ data: [KB], error: undefined })
    if (path === '/api/v1/conversations') {
      return Promise.resolve({ data: { items: [CONVERSATION], nextCursor: null }, error: undefined })
    }
    if (path === '/api/v1/agents') return Promise.resolve({ data: [AGENT], error: undefined })
    throw new Error(`用例没预料到的 GET ${path}`)
  })
}

/** 用一页壳子把"当前路径"渲染出来，断言导航结果时不依赖 router 内部状态。 */
function LocationProbe() {
  const location = useLocation()
  return <p data-testid="location">{location.pathname}</p>
}

function Host() {
  const [open, setOpen] = useState(false)
  return (
    <MemoryRouter initialEntries={['/']}>
      <button type="button" onClick={() => setOpen(true)}>
        打开命令面板
      </button>
      <CommandPalette open={open} onOpenChange={setOpen} />
      <LocationProbe />
    </MemoryRouter>
  )
}

function renderHost() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <Host />
    </QueryClientProvider>,
  )
}

/** 模拟 ⌘K / Ctrl+K：事件必须打到 window 上，监听器注册在那。 */
async function openWithShortcut() {
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true })
  return screen.findByRole('dialog')
}

beforeAll(() => {
  // jsdom 不实现这两样：Radix 的 Dialog 在真实浏览器里依赖它们做定位/滚动，
  // 缺了会在挂载阶段报 "not a function"（和 AgentDetailPage.test 里的桩同理）。
  Element.prototype.scrollIntoView = vi.fn()
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('CommandPalette', () => {
  afterEach(cleanup)
  beforeEach(() => {
    getMock.mockReset()
    mockLists()
  })

  it('Ctrl+K 打开面板并自动聚焦输入框', async () => {
    renderHost()
    await openWithShortcut()

    const input = await screen.findByRole('combobox', { name: '搜索命令' })
    await waitFor(() => expect(document.activeElement).toBe(input))

    // 空查询按组显示：四个分组标题都应该在（页面/知识库/会话/Agent + 动作）。
    expect(screen.getByRole('group', { name: '页面' })).toBeTruthy()
    expect(screen.getByRole('group', { name: '知识库' })).toBeTruthy()
    expect(screen.getByRole('group', { name: '会话' })).toBeTruthy()
    expect(screen.getByRole('group', { name: 'Agent' })).toBeTruthy()
    expect(screen.getByRole('group', { name: '动作' })).toBeTruthy()

    // 页面组是封闭集合，侧栏能进的页面这里必须全都有（不能被"每组最多 5 条"
    // 截掉最后一个——那会让"设置"只能靠背路径访问）。
    expect(screen.getByRole('option', { name: /设置/ })).toBeTruthy()

    // 第一条默认被选中（role=option 的 aria-selected），Enter 才有明确目标。
    const options = screen.getAllByRole('option')
    expect(options[0].getAttribute('aria-selected')).toBe('true')
  })

  it('输入过滤：子序列匹配，且只留下命中的条目', async () => {
    renderHost()
    await openWithShortcut()

    const input = await screen.findByRole('combobox', { name: '搜索命令' })
    fireEvent.change(input, { target: { value: 'transformer' } })

    expect(await screen.findByRole('option', { name: /Transformer 论文库/ })).toBeTruthy()
    // 不相关条目（Agent 名 / 页面名）必须消失，否则"过滤"就是摆设。
    expect(screen.queryByText('算数助手')).toBeNull()
    expect(screen.queryByText('设置')).toBeNull()
  })

  it('↑/↓ 移动选中项，Enter 执行导航', async () => {
    renderHost()
    await openWithShortcut()

    const input = await screen.findByRole('combobox', { name: '搜索命令' })
    // 默认选中第一项（知识库）；下移一次应该落到"对话"。
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    await waitFor(() => {
      const options = screen.getAllByRole('option')
      expect(options[1].getAttribute('aria-selected')).toBe('true')
    })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() =>
      expect(screen.getByTestId('location').textContent).toBe('/conversations'),
    )
    // 执行完就关，用户不会看到面板还挂在导航后的页面上。
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('输入后 Enter 直接执行唯一命中项（不用先按 ↓）', async () => {
    renderHost()
    await openWithShortcut()

    const input = await screen.findByRole('combobox', { name: '搜索命令' })
    fireEvent.change(input, { target: { value: '设置' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/settings'))
  })

  it('Esc 关闭面板，焦点回到打开它的按钮', async () => {
    renderHost()
    const trigger = screen.getByRole('button', { name: '打开命令面板' })
    trigger.focus()
    fireEvent.click(trigger)

    const dialog = await screen.findByRole('dialog')
    fireEvent.keyDown(dialog, { key: 'Escape' })

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // 不还回去的话，键盘用户下一次 Tab 会从页面开头重新走（README §14）。
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('无结果时给空态，而不是一个空白列表', async () => {
    renderHost()
    await openWithShortcut()

    const input = await screen.findByRole('combobox', { name: '搜索命令' })
    fireEvent.change(input, { target: { value: 'zzzzzz' } })

    expect(await screen.findByText('没有匹配的命令')).toBeTruthy()
    expect(screen.queryAllByRole('option')).toHaveLength(0)
  })
})
