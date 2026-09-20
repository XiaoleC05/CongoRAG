import { BarChart3, BookOpen, Bot, MessagesSquare, Moon, Settings, Sun } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { NavLink, Outlet, useLocation } from 'react-router'

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
          <Outlet />
        </main>
      </SidebarInset>
    </SidebarProvider>
  )
}
