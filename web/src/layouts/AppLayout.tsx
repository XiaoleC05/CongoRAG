import { BarChart3, BookOpen, Bot, MessagesSquare, Moon, Settings, Sun } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { Suspense } from 'react'
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

/**
 * 左侧导航项。
 *
 * `soon` 标记还没实现的模块——它们渲染成禁用状态并带 tooltip，
 * 而不是链到一个空白页。这样导航能反映完整的产品形态，
 * 又不会让人以为点了没反应是 bug。
 */
type NavItem = {
  to: string
  label: string
  icon: LucideIcon
  /** 未实现的模块标注所属里程碑，渲染成禁用项 */
  soon?: string
}

const NAV: NavItem[] = [
  { to: '/knowledge-bases', label: '知识库', icon: BookOpen },
  { to: '/conversations', label: '对话', icon: MessagesSquare, soon: 'M2' },
  { to: '/agents', label: 'Agent', icon: Bot },
  { to: '/usage', label: '用量', icon: BarChart3, soon: 'M5' },
]

export default function AppLayout() {
  const { pathname } = useLocation()
  const { theme, toggle } = useTheme()

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
                      tooltip={
                        item.soon ? `${item.label}（${item.soon} 实现）` : item.label
                      }
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
              <SidebarMenuButton
                disabled
                tooltip="设置（M1 引导页实现）"
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
        <main className="min-w-0 flex-1 overflow-auto">
          {/* 错误边界和 Suspense 都套在 <main>【里面】。
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
              <Outlet />
            </Suspense>
          </RouteErrorBoundary>
        </main>
      </SidebarInset>
    </SidebarProvider>
  )
}
