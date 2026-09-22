import { Moon, Settings, Sun } from 'lucide-react'
import { Suspense, useEffect, useRef } from 'react'
import type { RefObject } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router'

import { PageFallback } from '@/components/PageFallback'
import { RouteErrorBoundary } from '@/components/RouteErrorBoundary'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import { useTheme } from '@/hooks/useTheme'
import { NAV } from '@/layouts/nav'

/*
 * 导航表在 layouts/nav.ts：它是一个纯数据常量，放在这里会打断 Fast Refresh
 * （oxlint 的 react(only-export-components)），测试也够不着它。
 */

export default function AppLayout() {
  const { pathname } = useLocation()
  const { theme, toggle } = useTheme()
  // 主内容容器。除了当滚动容器，它还是路由切换后的焦点落点之一（见 RouteFocus）。
  const contentRef = useRef<HTMLDivElement>(null)

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <div className="flex items-center gap-2 px-2 py-1.5 text-sm font-semibold">
            <span className="bg-primary text-primary-foreground flex size-6 items-center justify-center rounded-md text-xs">
              C
            </span>
            <span className="group-data-[collapsible=icon]:hidden">ConGoRAG</span>
          </div>
        </SidebarHeader>

        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupLabel>导航</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {NAV.map((item) => (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton
                      asChild={!item.soon}
                      disabled={'soon' in item && !!item.soon}
                      isActive={!item.soon && pathname.startsWith(item.to)}
                      tooltip={item.soon ? `${item.label}（${item.soon}）` : item.label}
                    >
                      {item.soon ? (
                        <div>
                          <item.icon />
                          <span>{item.label}</span>
                        </div>
                      ) : (
                        <NavLink to={item.to}>
                          <item.icon />
                          <span>{item.label}</span>
                        </NavLink>
                      )}
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>

        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              {/* 【这一项刻意保持禁用，不要顺手给它接页面】设置页属于
                  「provider / 模型管理」那条线（issue #83），本批次不实现。
                  原来的 tooltip 写的是"设置（M1 引导页实现）"——引导页不是设置，
                  那是一句假信息。现在写事实：还没有。
                  留着一个禁用的占位是为了让用户知道"这里以后会有东西"，
                  而不是留一个点了没反应、或者跳 404 的死链接。 */}
              <SidebarMenuButton
                disabled
                tooltip="设置（尚未实现）"
                className="group-data-[collapsible=icon]:justify-center"
              >
                <Settings />
                <span>设置</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <SidebarMenuButton onClick={toggle} tooltip={theme === 'dark' ? '切到浅色' : '切到深色'}>
                {theme === 'dark' ? <Sun /> : <Moon />}
                <span>{theme === 'dark' ? '浅色' : '深色'}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>

        <SidebarRail />
      </Sidebar>

      <SidebarInset>
        {/* 主区域的容器必须是 min-w-0 的 flex 列：
            侧栏用固定宽度，主区域里有长表格/长代码块时不会把它撑变形。 */}
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger />
        </header>
        {/* 【为什么是 div 而不是 <main>（issue #94 顺手修的）】页面上已经有一个
            <main> 了：shadcn 的 SidebarInset 自己就渲染一个 `<main>`（见
            components/ui/sidebar.tsx）。再套一个就有两个 main landmark——
            读屏软件按地标跳转的人会撞见两个"主要内容区"，axe 也把它算成
            landmark-no-duplicate-main。所以这里降成 div：整页仍然只有一个
            main 地标（SidebarInset 那个，标题和内容都在它里面），
            而 flex 布局、滚动、焦点落点都不受影响。

            tabIndex={-1}：让这个容器可以被脚本聚焦（页面没有 h1 时的落点，
            见 RouteFocus），但不进 Tab 序列——键盘用户不会在 Tab 路上撞到它。
            它上面的聚焦框由 index.css 里那条 [tabindex='-1']:focus 规则去掉。 */}
        <div ref={contentRef} tabIndex={-1} className="min-w-0 flex-1 overflow-auto">
          {/* 错误边界和 Suspense 都套在这个滚动容器【里面】。
              【为什么不能套到 <Routes> 外面】套在外面的话，页面 chunk 没到时
              连侧栏和顶栏一起消失，整屏变骨架——那是"应用崩了"的样子，
              不是"内容在加载"。反过来，这里的 min-w-0 和 flex-1 必须留在
              边界外面：它们靠的是父级 flex 容器的直接子项身份，
              中间多插一层就会把侧栏撑变形（README 的"已知的坑"里记过这条）。

              【key={pathname}】错误边界一旦记下 error 就不会自己复位。
              不加 key 的话，一次 chunk 404 会把之后访问的每个页面都盖住——
              用户点了别的导航，看到的还是刚才那个报错。换 key 换实例。 */}
          <RouteErrorBoundary key={pathname}>
            <Suspense fallback={<PageFallback />}>
              <RouteFocus containerRef={contentRef} />
              <Outlet />
            </Suspense>
          </RouteErrorBoundary>
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}

/**
 * 路由切换后把焦点搬到新页面上（issue #94）。
 *
 * 【这是 SPA 的固有问题】多页应用换页时浏览器会把焦点和阅读位置重置到文档开头，
 * SPA 不会：焦点留在原来那个元素上（通常是刚点过的侧栏链接），读屏软件也不知道
 * 页面已经换了。键盘用户按下的下一次 Tab 会从侧栏接着往下走，而不是从新页面的
 * 开头开始——他能操作，但找不对地方。
 *
 * 【为什么挂在 <Suspense> 【里面】】页面 chunk 没到时这一层根本不会挂载
 * （Suspense 用 fallback 顶掉整棵子树），所以它的 effect 只在"页面内容已经在
 * DOM 里"之后才跑——那时候页面自己的 h1 才查得到。挂在边界外面的话，焦点会先
 * 落在还只有骨架的内容容器上，等页面到了也不会再搬一次，标题就永远没机会被播报。
 *
 * 【为什么优先聚焦 h1，而不是只聚焦容器】聚焦容器时读屏软件只念得出"main"，
 * 用户知道换了区域、不知道换到了哪一页。h1 会把标题念出来，这也是 h1 是每个
 * 页面必需的原因（见 web/README.md 的规范 §12）。页面没有 h1 时（当前的例外
 * 只有对话页，理由记在 §12）退回聚焦主内容容器——至少阅读位置回到了开头。
 *
 * 【不产生视觉噪声】h1 与容器都靠 tabindex="-1" 变成"只能被脚本聚焦"，
 * 它们不进 Tab 序列，聚焦框也由 CSS 去掉（index.css），所以看不出焦点搬过。
 */
function RouteFocus({ containerRef }: { containerRef: RefObject<HTMLElement | null> }) {
  const { pathname } = useLocation()

  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    // 【已经落在新页面里的焦点不抢】对话页与 Agent 详情页的输入框带 autoFocus：
    // 用户点开一个会话，就该能直接开始打字。把焦点搬到标题上等于让他每次多按一下，
    // 而且是那种不报错的坏法——没有任何测试会发现。
    // autoFocus 在提交阶段就生效了，effect 跑的时候它已经在 activeElement 上。
    const active = document.activeElement
    if (active && container.contains(active)) return

    const heading = container.querySelector('h1')
    if (heading) {
      // h1 天生不可聚焦，要补一个 tabindex="-1" 才能 .focus()。
      // 这里用 JS 补而不是写在各页面的 JSX 上，是为了让"新页面只要有个 h1 就
      // 自动接上焦点管理"——页面侧不需要知道这件事，也就不会漏。
      // React 不管理这个属性（没写成 prop），后续渲染不会把它抹掉。
      heading.tabIndex = -1
      heading.focus()
      return
    }

    container.focus()
  }, [pathname, containerRef])

  return null
}
