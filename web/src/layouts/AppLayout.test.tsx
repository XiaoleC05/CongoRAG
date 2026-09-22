// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { lazy } from 'react'
import { Link, MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import AppLayout from '@/layouts/AppLayout'
import { NAV } from '@/layouts/nav'

/**
 * 永不 resolve 的 lazy，用来把"chunk 还在下载"这一帧钉住。
 *
 * 【为什么不用一个真的写慢一点的假 chunk】那要靠下载时序去赌，
 * 快机器上 chunk 已经下完、慢机器上还没开始，用例会变成不稳定的。
 * 一个永远不 settle 的 Promise 把这一刻变成确定状态。
 */
const NeverReadyPage = lazy(() => new Promise<{ default: () => null }>(() => {}))

/** src/pages/ 下现有的页面文件（只取文件名，不真的加载它们）。 */
const PAGE_FILES = Object.keys(import.meta.glob('../pages/*.tsx')).filter(
  (path) => !path.includes('.test.'),
)

/**
 * 把页面文件名与导航路径归一成同一个形状，用来比对"NAV 里说没实现的模块，
 * 到底有没有页面"。`KnowledgeBasesPage.tsx` → `knowledgebase`，
 * `/knowledge-bases` → `knowledgebase`；`ConversationPage.tsx` → `conversation`，
 * `/conversations` → `conversation`（末尾的复数 s 去掉，否则单复数对不上）。
 */
const normalize = (name: string) =>
  name
    .replace(/Page\.tsx$/, '')
    .replace(/^.*\//, '')
    .replace(/[^a-z0-9]/gi, '')
    .toLowerCase()
    .replace(/s$/, '')

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

/**
 * 一组静态页面，用来测焦点管理。
 *
 * 【为什么不用 NeverReadyPage】焦点落点要等页面内容真的在 DOM 里才找得到，
 * 那正是 RouteFocus 的作用；而且焦点管理的三条路径（页面有 h1 / 页面没有 h1 /
 * 页面自己已经拿到了焦点）都需要真实的页面内容才能摆出来。
 */
function renderRoutes(initialEntry: string) {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route element={<AppLayout />}>
            <Route path="/knowledge-bases" element={<h1>知识库列表</h1>} />
            <Route
              path="/agents"
              element={
                <>
                  <h1>Agent 列表</h1>
                  {/* 真实应用里进会话是「开始对话」按需跳转
                      （KnowledgeBaseDetailPage），这里用一条链接复现
                      "从一个页面跳到带 autoFocus 的页面"。 */}
                  <Link to="/conversations/1">打开会话</Link>
                </>
              }
            />
            {/* 没有 h1、但有 autoFocus 的页面（对话页就是这个形状，
                见 pages/headingStructure.test.ts 的例外清单）：
                焦点留在输入框里，不被标题抢走。 */}
            <Route path="/conversations/:id" element={<input autoFocus placeholder="输入消息…" />} />
            {/* 既没有 h1 也没有 autoFocus 的合成页面。应用里目前没有这样的一页，
                但它是"新页面忘了写 h1"时唯一的兜底路径：焦点至少要落在主内容
                容器上，键盘用户的 Tab 才是从新页面开始的。 */}
            <Route path="/no-heading" element={<p>没有标题的页面</p>} />
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

describe('侧栏导航项与已实现模块的一致性', () => {
  afterEach(cleanup)

  // 回归（issue #75）：对话页早就路由可用（router.tsx 的 conversations/:id），
  // 侧栏却还挂着 soon: 'M2'，把它渲染成一个禁用项 + "（M2 实现）"的 tooltip。
  // 用户第一眼看到的入口在告诉用户"这个功能还没做"。
  it('对话是可点的链接，不是禁用占位', () => {
    renderInAppLayout()

    expect(screen.getByRole('link', { name: '对话' }).getAttribute('href')).toBe('/conversations')
  })

  it('对话在 /conversations/* 下是高亮态', () => {
    // 激活态判据是 pathname.startsWith(item.to)，路由带参数（会话 id）时
    // 也必须亮：点进一个会话，侧栏要指出"你在对话这一块"。
    renderRoutes('/conversations/1')

    expect(screen.getByRole('link', { name: '对话' }).getAttribute('data-active')).toBe('true')
    // 同时钉住"别的项不亮"：只断言一个 true 的话，
    // 将来 isActive 写成恒真也照样过。
    expect(screen.getByRole('link', { name: '知识库' }).getAttribute('data-active')).toBe('false')
  })

  // 【这条原来是"用量仍然是禁用占位"，2026-09-22 随 issue #76 反转】
  // UsagePage 落地之后，nav.ts 里 /usage 那一项的 soon 摘掉了，于是它从
  // 禁用项变回一个真实的链接。上面「对话」那条钉的是同一个规则的两个实例：
  // **页面在，入口就不能还是禁用占位**。所以这条改成和它对称的写法，
  // 而不是删掉——删掉的话，"用量又变回占位"就没人拦得住了。
  it('用量是可点的链接，不是禁用占位', () => {
    renderInAppLayout()

    expect(screen.getByRole('link', { name: '用量' }).getAttribute('href')).toBe('/usage')
  })

  // 上面两条钉的是今天的状态，这一条钉的是规则本身：**NAV 里标了 soon 的模块，
  // src/pages/ 下就必须真的没有对应页面**。这样一来，将来谁实现了 UsagePage 却
  // 忘了摘 soon，或者反过来给已经能用的模块加上 soon 占位，都会红。
  it('标了 soon 的模块确实没有页面文件', () => {
    const pageKeys = PAGE_FILES.map(normalize)

    for (const item of NAV) {
      if (!item.soon) continue
      const key = normalize(item.to)
      expect(pageKeys).not.toContain(key)
    }
  })
})

describe('路由切换后的焦点管理（issue #94）', () => {
  afterEach(cleanup)

  // 回归：AppLayout 里原来在 SidebarInset（shadcn 生成的 <main>）里面又套了一个
  // <main>，于是每个页面都有两个 main 地标——读屏软件按地标跳转的人会撞见
  // 两个"主要内容区"。内容容器已降成 div，这条钉住它别再被改回标签。
  it('整页只有一个 main 地标', () => {
    renderRoutes('/agents')

    expect(screen.getAllByRole('main')).toHaveLength(1)
  })

  // SPA 换页不会像多页应用那样把焦点重置到文档开头：焦点留在刚点过的侧栏链接上，
  // 键盘用户的下一次 Tab 会从侧栏继续走，读屏软件也不会播报页面换了。
  it('进入页面时焦点落在页面 h1 上', () => {
    renderRoutes('/agents')

    expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Agent 列表' }))
  })

  it('切换路由后焦点跟着搬到新页面的 h1', async () => {
    renderRoutes('/agents')

    fireEvent.click(screen.getByRole('link', { name: '知识库' }))

    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole('heading', { level: 1, name: '知识库列表' }),
      ),
    )
  })

  it('页面自己已经拿到的焦点不被抢走（对话页的输入框）', async () => {
    renderRoutes('/agents')

    fireEvent.click(screen.getByRole('link', { name: '打开会话' }))

    const input = screen.getByPlaceholderText('输入消息…')
    // autoFocus 在提交阶段就生效了，RouteFocus 的 effect 之后才跑：
    // 看到焦点已经在新页面里，它就不动。抢走的话，用户点开会话还得先点一下输入框，
    // 而且这种坏法不会报任何错。
    await waitFor(() => expect(document.activeElement).toBe(input))
  })

  it('页面没有 h1 时焦点落在主内容容器上，且容器不进 Tab 序列', () => {
    renderRoutes('/no-heading')

    // 用"包着页面内容的那个 tabindex=-1 元素"来认容器，而不是按标签名查：
    // 页面上唯一的 <main> 是 shadcn 的 SidebarInset，内容容器是一个 div
    // （两个 main landmark 是缺陷，见 AppLayout.tsx 里的注释）。
    const container = screen.getByText('没有标题的页面').closest('[tabindex="-1"]')
    expect(document.activeElement).toBe(container)
    // tabindex="-1"：能被脚本聚焦、不能被 Tab 走到。落点的聚焦框由
    // index.css 的 [tabindex='-1']:focus 去掉，所以焦点搬过去看不出来。
    expect(container?.getAttribute('tabindex')).toBe('-1')
  })
})
