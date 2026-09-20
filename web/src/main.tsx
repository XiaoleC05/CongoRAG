import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

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
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <AppRouter />
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
)
