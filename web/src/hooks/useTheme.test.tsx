// @vitest-environment jsdom
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { useTheme } from '@/hooks/useTheme'

/**
 * 同一个组件里挂两个 useTheme()，模拟"侧栏一个开关 + 页面里另一处也读主题"
 * 的形态（第二个消费者出现的那一刻，useState 版本的问题就会露出来）。
 */
function useTwoThemes() {
  return { sidebar: useTheme(), content: useTheme() }
}

describe('useTheme', () => {
  // readTheme 读的是 <html> 上的类名，不是任何模块内状态——
  // 上一个用例留下的 DOM/localStorage 会直接变成下一个用例的初始状态，
  // 所以每个用例前后都要把环境恢复到"index.html 的原样"（深色为默认）。
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove('light')
    document.documentElement.classList.add('dark')
  })

  afterEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove('light')
    document.documentElement.classList.add('dark')
  })

  // 回归：原来 useTheme 是 useState(readTheme)，每个实例各存一份快照。
  // toggle 只改自己那个实例的状态，第二个实例永远停留在挂载时读到的值
  // （它不会再去读 DOM），于是"在侧栏切了主题，页面里另一处一直是旧的"，
  // 而且是永久性的、不报错的。
  it('两个实例共享同一份主题：一处 toggle，两处都要变', () => {
    const { result } = renderHook(() => useTwoThemes())
    expect(result.current.sidebar.theme).toBe('dark')
    expect(result.current.content.theme).toBe('dark')

    act(() => {
      result.current.sidebar.toggle()
    })

    // (a) 两个实例都跟着变了——修复前 content.theme 仍然是 'dark'
    expect(result.current.sidebar.theme).toBe('light')
    expect(result.current.content.theme).toBe('light')

    // (b) DOM 和 localStorage 都要和实例读到的值一致：
    // 主题的真相只有一个（<html> 的类名），实例不能各说各话。
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    expect(localStorage.getItem('congorag-theme')).toBe('light')
  })
})
