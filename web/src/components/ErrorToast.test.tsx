// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { ErrorToast } from '@/components/ErrorToast'

// 没有配 globals，@testing-library 的自动清理不会生效（它靠全局的 afterEach），
// 必须自己调，否则上一个用例的 DOM 会留在 document 里干扰 getByRole。
afterEach(cleanup)

describe('ErrorToast', () => {
  // 这是这个组件存在的理由：sonner 的 <li data-sonner-toast> 上没有 role，
  // 只有一个 aria-live="polite" 的容器。错误要立刻播报，靠的就是这一行。
  it('根节点带 role="alert"，且不同时带 aria-live', () => {
    render(<ErrorToast error={{ type: 'internal_error', detail: 'boom' }} />)

    const alert = screen.getByRole('alert')
    // role="alert" 已经隐含 assertive；再加 aria-live 会让部分读屏软件
    // 认为是两个区域，同一条内容念两遍
    expect(alert.hasAttribute('aria-live')).toBe(false)
  })

  it('有 type 映射时，主文案来自 MESSAGES，后端 detail 作为第二行', () => {
    render(<ErrorToast error={{ type: 'internal_error', detail: 'boom' }} />)

    const alert = screen.getByRole('alert')
    const [primary, secondary] = alert.querySelectorAll('p')

    // 主文案是 lib/errors.ts 里那句中文，不是后端原文
    expect(primary?.textContent).toBe('服务内部错误')
    expect(secondary?.textContent).toBe('boom')
  })

  it('每个已知 type 都渲染出人话，不漏空白', () => {
    for (const type of [
      'invalid_argument',
      'not_found',
      'conflict_duplicate_key',
      'conflict',
      'context_overflow',
      'upstream_llm_error',
      'internal_error',
    ]) {
      const { unmount } = render(<ErrorToast error={{ type }} />)
      const text = screen.getByRole('alert').textContent
      expect(text).toBeTruthy()
      expect(text).not.toBe(type)
      unmount()
    }
  })

  it('认不出的 type 回退到后端原文，且不重复显示成两行', () => {
    render(<ErrorToast error={{ type: 'brand_new_type', detail: 'something broke' }} />)

    const alert = screen.getByRole('alert')
    // 主文案已经用了 detail，就不该再补一行一模一样的
    expect(alert.querySelectorAll('p')).toHaveLength(1)
    expect(alert.textContent).toBe('something broke')
  })

  it('没有 detail 时只渲染一行，不留空白行', () => {
    render(<ErrorToast error={{ type: 'not_found' }} />)

    const alert = screen.getByRole('alert')
    expect(alert.querySelectorAll('p')).toHaveLength(1)
    expect(alert.textContent).toBe('要找的东西不存在')
  })

  it('非 Problem 形状的失败也能渲染出可读的一句话', () => {
    render(<ErrorToast error={new TypeError('Failed to fetch')} />)

    expect(screen.getByRole('alert').textContent).toContain('Failed to fetch')
  })
})
