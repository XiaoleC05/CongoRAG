import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AppRouter } from '@/router'
import './index.css'

// QueryClient 管理缓存、重试和后台刷新。
// 不用它的话，每个页面都要自己用 useState + useEffect 处理
// loading / error / 缓存 / 重试 / 竞态。
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 关掉自动重试：默认失败会重试 3 次，后端没起来时要等好几秒才看到错误。
      retry: false,
      // 窗口重新获得焦点时不自动重拉，避免开发期频繁打日志
      refetchOnWindowFocus: false,
    },
  },
})

// Provider 的嵌套顺序：
//   QueryClientProvider  —— useQuery 是往下找最近的 Provider 拿 queryClient 的
//   TooltipProvider      —— SidebarProvider 不内置它，而侧栏折叠成图标时要用 tooltip
//   AppRouter            —— 路由在布局路由里渲染 AppLayout（含 SidebarProvider）
//   Toaster              —— 写操作失败的出口
//
// 【Toaster 必须挂在路由树外面】引导页（OnboardingPage）是挂在 AppLayout
// 之外的独立路由（见 router.tsx），它的保存失败正是 toast 要覆盖的场景之一。
// 塞进 AppLayout 或任何布局路由里，那一页的失败就没人接。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <AppRouter />
        <Toaster />
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
)
