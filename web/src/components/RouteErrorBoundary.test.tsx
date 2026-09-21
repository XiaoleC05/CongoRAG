// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { lazy } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { RouteErrorBoundary } from '@/components/RouteErrorBoundary'

/**
 * 模拟 lazy 的 chunk 404。
 *
 * 报错文本是浏览器的原话（Chrome/Safari 都是这一句），不是我们自己编的：
 * 换版本时二进制被替换，而浏览器里还开着上一版的 index.html，
 * 那页 HTML 指向的 chunk 名带 hash，构建时的 emptyOutDir 已经把它们删了，
 * 于是 import() 失败并抛出这句话。
 */
const BrokenPage = lazy(() =>
  Promise.reject<{ default: () => null }>(
    new Error('Failed to fetch dynamically imported module'),
  ),
)

describe('RouteErrorBoundary', () => {
  // 边界把错误吞掉再渲染兜底界面，React 和这个组件自己都会往控制台打一笔。
  // 用例要断言的是界面而不是日志——静音，免得这些噪声淹掉真正的失败信息。
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  // 回归：这个组件在修复前根本不存在，用例连 import 都过不去（模块找不到）。
  // 它要挡住的是"lazy 的 import() reject 时整棵树被卸载成白屏"——
  // 没有边界时页面全白，控制台只有一行 chunk 报错，比一次普通崩溃更难查。
  it('chunk 加载失败时渲染可操作的报错，而不是白屏', async () => {
    render(
      <RouteErrorBoundary>
        <BrokenPage />
      </RouteErrorBoundary>,
    )

    // reject 是异步的（import() 本来就是），等边界把兜底界面换上来
    expect(await screen.findByRole('alert')).toBeTruthy()

    // React 会把 lazy 的失败结果缓存住，重试渲染恢复不了，只能整页重载——
    // 所以这个按钮是必需的出口，不是装饰。
    expect(screen.getByRole('button', { name: '刷新页面' })).toBeTruthy()

    // 浏览器给的原文要直接摆在界面上，否则排查时还得让用户开控制台
    expect(screen.getByText(/Failed to fetch dynamically imported module/)).toBeTruthy()
  })
})
