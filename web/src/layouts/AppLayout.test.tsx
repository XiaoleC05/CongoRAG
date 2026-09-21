// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { lazy } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import AppLayout from '@/layouts/AppLayout'

/**
 * 永不 resolve 的 lazy，用来把"chunk 还在下载"这一帧钉住。
 *
 * 【为什么不用一个真的写慢一点的假 chunk】那要靠下载时序去赌，
 * 快机器上 chunk 已经下完、慢机器上还没开始，用例会变成不稳定的。
 * 一个永远不 settle 的 Promise 把这一刻变成确定状态。
 */
const NeverReadyPage = lazy(() => new Promise<{ default: () => null }>(() => {}))

function renderInAppLayout() {
  return render(
    // TooltipProvider 的位置和 main.tsx 一致：SidebarProvider 不内置它，
    // 而侧栏的每个菜单项都带 tooltip，缺了它会直接报 Provider 缺失。
    <TooltipProvider>
      <MemoryRouter initialEntries={['/agents']}>
        <Routes>
          <Route element={<AppLayout />}>
            <Route path="/agents" element={<NeverReadyPage />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

describe('AppLayout 的 Suspense 边界', () => {
  // 没有配 globals，@testing-library 的自动清理不生效（它靠全局的 afterEach），
  // 所以和项目里其它测试文件一样显式清理。
  afterEach(cleanup)

  // 回归：AppLayout 原先在 <main> 里直接渲染 <Outlet />，没有任何 Suspense。
  // 一旦某个路由元素是 lazy 的、它的 chunk 还没到，React 在整棵树里找不到
  // 边界，就把"挂起"一路抛到根部——这一帧既没有页面骨架，也没有侧栏，
  // 整屏空白。用户看到的是"点了导航，界面整个没了"，
  // 而控制台只有一行 chunk 相关的报错，指不到任何业务代码。
  it('页面 chunk 未就绪时渲染骨架，且侧栏仍然挂载', () => {
    renderInAppLayout()

    // (a) 内容区换成兜底骨架
    expect(screen.getByTestId('page-fallback')).toBeTruthy()

    // (b) 侧栏没有被一起拿掉。两条断言必须同时成立才说明边界在 <main> 里面：
    //     把边界套到 <Routes> 外面时 (a) 照样过，但 (b) 会挂。
    expect(screen.getByText('ConGoRAG')).toBeTruthy()
    expect(screen.getByText('知识库')).toBeTruthy()
  })
})
