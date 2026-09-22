// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CopyButton } from '@/components/conversation/CopyButton'
import { Toaster } from '@/components/ui/sonner'

// Toaster 的主题来自 useTheme。这个用例测的是"复制走哪条通道"，
// 和主题无关——把它钉住，免得那边的改动让这里无辜变红。
vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({ theme: 'dark', toggle: () => {} }),
}))

/**
 * 替换 navigator.clipboard。
 *
 * 【为什么必须用 defineProperty】jsdom 不实现剪贴板，navigator 上根本没有
 * clipboard 这个属性——直接赋值是给原型加属性，测"属性不存在"那条用例时
 * 会互相污染。configurable: true 才能让下一个用例把它改回去。
 */
function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
}

function renderButton() {
  return render(
    <>
      <CopyButton text="要复制的内容" label="复制回答" />
      <Toaster />
    </>,
  )
}

afterEach(() => {
  // sonner 的 toast 队列是模块级的，不清会漏到下一个用例里
  toast.dismiss()
  setClipboard(undefined)
  cleanup()
})

describe('CopyButton', () => {
  it('复制成功：写入剪贴板并给出一次 toast 回执', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })

    renderButton()
    fireEvent.click(screen.getByRole('button', { name: '复制回答' }))

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith('要复制的内容')
      expect(screen.getByText('已复制回答')).toBeTruthy()
    })
  })

  // 交付期前端由 Go 内嵌在 :3210 上提供，默认是 http（非安全上下文）
  // ——navigator.clipboard 是 undefined，不是"调用会抛"。这条路径真会走到。
  it('非安全上下文（没有 navigator.clipboard）也要有明确提示，不静默失败', async () => {
    setClipboard(undefined)

    renderButton()
    fireEvent.click(screen.getByRole('button', { name: '复制回答' }))

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('剪贴板')
    })
  })

  it('权限被拒时同样有提示', async () => {
    const writeText = vi.fn().mockRejectedValue(new DOMException('denied', 'NotAllowedError'))
    setClipboard({ writeText })

    renderButton()
    fireEvent.click(screen.getByRole('button', { name: '复制回答' }))

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('剪贴板权限')
    })
  })

  it('按钮是可聚焦的真控件，带对象名的可访问名', () => {
    setClipboard(undefined)
    renderButton()

    const button = screen.getByRole('button', { name: '复制回答' })
    expect(button.tagName).toBe('BUTTON')
    // 没有写成 tabIndex={-1}：键盘必须能走到它（README §14）
    expect(button.getAttribute('tabindex')).toBeNull()
  })
})
