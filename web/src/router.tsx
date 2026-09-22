import { lazy, Suspense } from 'react'
import { BrowserRouter, Navigate, Outlet, Route, Routes } from 'react-router'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ErrorText } from '@/components/ErrorText'
import { PageFallback } from '@/components/PageFallback'
import { RouteErrorBoundary } from '@/components/RouteErrorBoundary'
import { useProviders } from '@/hooks/useProviders'
import AppLayout from '@/layouts/AppLayout'
import KnowledgeBasesPage from '@/pages/KnowledgeBasesPage'
import NotFoundPage from '@/pages/NotFoundPage'

// 除落地页和 404 之外的页面都按路由懒加载：每个 lazy() 切成一个独立 chunk，
// 只有真的走到那条路由时才下载。
//
// 【为什么 lazy() 写在模块作用域，绝不能写进 AppRouter() 里】
// 在组件函数体里调 lazy()，每次渲染都会造出一个【全新的组件类型】。
// React 比的是引用，于是它认为"这是个不同的组件"，把整棵子树卸载重挂：
// 输入框里的草稿、正在流式的回答、滚动位置全丢。这个 bug 不报错，
// 表现为"切个路由回来，我刚打的东西没了"——看起来像玄学。
// lazy() 的结果必须跨渲染稳定，所以只能在模块作用域算这一次。
//
// 【Suspense 边界放在哪】主界面那几条路由的边界在 AppLayout 的内容容器里面
// （见 layouts/AppLayout.tsx），不是包在 <Routes> 外面：包在外面的话，
// 页面 chunk 没到时连侧栏一起消失，整屏变骨架——那看起来像应用崩了。
// OnboardingPage 是挂在 AppLayout 之外的独立路由（它没有侧栏），
// 所以它的路由元素得自己带一个 <Suspense>。
//
// 【为什么落地页和 404 页保持静态导入】index 会重定向到 /knowledge-bases，
// 它是绝大多数会话打开时命中的第一条路由，拆出去只是把一次下载提前变成
// "先下载入口、再下载它"，多一次往返还多了首帧骨架。NotFoundPage 的源码
// 只有几百字节，单独成 chunk 是净亏。
const OnboardingPage = lazy(() => import('@/pages/OnboardingPage'))
const KnowledgeBaseDetailPage = lazy(() => import('@/pages/KnowledgeBaseDetailPage'))
const ConversationsPage = lazy(() => import('@/pages/ConversationsPage'))
const ConversationPage = lazy(() => import('@/pages/ConversationPage'))
const AgentsPage = lazy(() => import('@/pages/AgentsPage'))
const AgentDetailPage = lazy(() => import('@/pages/AgentDetailPage'))
const RunTracePage = lazy(() => import('@/pages/RunTracePage'))
const UsagePage = lazy(() => import('@/pages/UsagePage'))
const SettingsPage = lazy(() => import('@/pages/SettingsPage'))

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
            全屏向导，不带侧栏——这时候用户还没有任何知识库/会话可看。
            也正因为不在 AppLayout 里，它拿不到 <main> 那个 Suspense 边界，
            所以这里必须自己包一层，否则 chunk 没到时整屏空白。 */}
        <Route
          path="onboarding"
          element={
            // 【错误边界也要自己包】AppLayout 里那个 RouteErrorBoundary 是懒加载
            // chunk 失败时唯一的兜底，而引导页在 AppLayout 之外。"发布后白屏、
            // 只有一个刷新按钮能救"这条对首跑用户尤其重要——他们看到的正好是
            // 这一页。
            <RouteErrorBoundary>
              <Suspense fallback={<PageFallback />}>
                <OnboardingPage />
              </Suspense>
            </RouteErrorBoundary>
          }
        />

        <Route element={<RequireProvider />}>
          <Route element={<AppLayout />}>
            <Route index element={<Navigate to="/knowledge-bases" replace />} />
            <Route path="knowledge-bases" element={<KnowledgeBasesPage />} />
            <Route path="knowledge-bases/:id" element={<KnowledgeBaseDetailPage />} />
            {/* 【conversations 这条为什么是新增的（issue #78）】在这之前
                路由表里只有 conversations/:id。侧栏「对话」那一项指向的是
                /conversations，于是点它落到 `*` 上——用户看到的是 404。
                会话列表页落地之后这一条才有内容，顺带把那个 404 修掉了。 */}
            <Route path="conversations" element={<ConversationsPage />} />
            <Route path="conversations/:id" element={<ConversationPage />} />
            <Route path="agents" element={<AgentsPage />} />
            <Route path="agents/:id" element={<AgentDetailPage />} />
            <Route path="agents/:agentId/runs/:runId" element={<RunTracePage />} />
            <Route path="usage" element={<UsagePage />} />
            <Route path="settings" element={<SettingsPage />} />
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
 *
 * 【isFetching 只在"结论还是空"时才算数】缓存里可能正躺着上一次（空库时）
 * 拉到的空数组：刚在引导页保存完 provider 跳回来时就是这个状态——缓存是旧的
 * []，一次重新拉取正在飞。只看 isPending 的话这里会拿旧数据直接判定"没配置过"，
 * 把刚保存成功的用户弹回一个空表单。
 *
 * 【为什么不能无条件把 isFetching 也拦下来】isFetching 对任何请求在飞都为真，
 * 包括后台重新拉取（refetchOnReconnect 默认开着）。已经有数据还把子路由拿掉，
 * 会把整棵子树卸载重挂：输入框里的草稿、正在流式的回答、侧栏展开状态全没了。
 * 所以只在"缓存里还没有任何 provider"这一种情况下等——那正是跳回来的那一刻。
 *
 * 写成 data?.length === 0 而不是 data.length === 0：请求失败且正在重试时
 * data 是 undefined，这里必须放过去交给下面的 error 分支，而不是显示"加载中"。
 */
export function RequireProvider() {
  const { data, isPending, isFetching, error } = useProviders()

  if (isPending || (isFetching && data?.length === 0)) {
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
