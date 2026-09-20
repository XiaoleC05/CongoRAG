import { BrowserRouter, Navigate, Outlet, Route, Routes } from 'react-router'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ErrorText } from '@/components/ErrorText'
import { useProviders } from '@/hooks/useProviders'
import AppLayout from '@/layouts/AppLayout'
import AgentDetailPage from '@/pages/AgentDetailPage'
import AgentsPage from '@/pages/AgentsPage'
import ConversationPage from '@/pages/ConversationPage'
import KnowledgeBaseDetailPage from '@/pages/KnowledgeBaseDetailPage'
import KnowledgeBasesPage from '@/pages/KnowledgeBasesPage'
import NotFoundPage from '@/pages/NotFoundPage'
import OnboardingPage from '@/pages/OnboardingPage'
import RunTracePage from '@/pages/RunTracePage'

/**
 * 路由表。
 *
 * 【为什么用声明式模式而不是 createBrowserRouter】
 * 数据获取已经由 TanStack Query 负责。数据路由的 loader/action 是另一套数据层，
 * 两套并存会让"数据从哪来"变得难讲。官方对声明式模式的定位里明确包含
 * "apps using their own data-sync abstractions"。
 *
 * 【布局路由不写 path】
 * 无 path 的 <Route> 是布局路由：它不消耗路径段，只负责保持自身挂载，
 * 子路由通过 <Outlet /> 渲染进去。切页面时 AppLayout 的实例被复用，
 * 侧栏的 DOM 和展开状态不会重建。
 *
 * 【import 路径】
 * v8 里 react-router-dom 已经不存在，一切都从 react-router 导入。
 * 唯一的例外是数据路由的 RouterProvider（在 react-router/dom），我们没用到。
 */
export function AppRouter() {
  return (
    <BrowserRouter>
      <Routes>
        {/* 引导页独立于 AppLayout：技术方案 §4.1 把它画成"首次打开"的
            全屏向导，不带侧栏——这时候用户还没有任何知识库/会话可看。 */}
        <Route path="onboarding" element={<OnboardingPage />} />

        <Route element={<RequireProvider />}>
          <Route element={<AppLayout />}>
            <Route index element={<Navigate to="/knowledge-bases" replace />} />
            <Route path="knowledge-bases" element={<KnowledgeBasesPage />} />
            <Route path="knowledge-bases/:id" element={<KnowledgeBaseDetailPage />} />
            <Route path="conversations/:id" element={<ConversationPage />} />
            <Route path="agents" element={<AgentsPage />} />
            <Route path="agents/:id" element={<AgentDetailPage />} />
            <Route path="agents/:agentId/runs/:runId" element={<RunTracePage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </BrowserRouter>
  )
}

/**
 * 主界面的守卫：一个 provider 都没配置过时，把用户送回引导页。
 *
 * 【判据只看"有没有"，不看"配得对不对"】配置是否真的可用（Key 有效、
 * 模型能调）在真正发起请求时才知道——这里只负责"跳过引导页会不会看到
 * 一个没法用的空壳界面"这一层，不是配置校验。
 *
 * 【失败态不重定向】请求 /api/v1/providers 本身失败（比如后端连不上数据库）
 * 和"确实没有配置过"是两件不同的事——把错误也重定向到引导页会让用户
 * 反复填一遍表单，永远看不到真正的错误原因。
 */
function RequireProvider() {
  const { data, isPending, error } = useProviders()

  if (isPending) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <p className="text-muted-foreground text-sm">加载中…</p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="mx-auto max-w-xl p-6">
        <Alert variant="destructive">
          <AlertTitle>无法确认模型配置状态</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  if (data.length === 0) {
    return <Navigate to="/onboarding" replace />
  }

  return <Outlet />
}
