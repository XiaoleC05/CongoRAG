import { Link } from 'react-router'

import { Button } from '@/components/ui/button'

/**
 * 前端路由没匹配上时的兜底页面。
 *
 * 注意：这只覆盖"进入了 React 但路由表里没有"的情况。
 * 服务端那条兜底在 apps/api/internal/api/spa.go 的 MountSPA——
 * 它让任何未匹配路径返回 index.html，前端才有机会渲染这一页。
 *
 * 【"这个页面不存在"是 h1，不是一句普通文案（issue #95）】这一页确实有主标题，
 * 原来写成 <p> 是一处遗漏（视觉上像标题、语义上不是）。改成 h1 后视觉没有变化
 * （字号字重来自 font-medium / 默认继承，和原来一样），但读屏软件的大纲里
 * 这一页有了标题，路由切换后的焦点也有地方可落（见 AppLayout 的 RouteFocus）。
 *
 * 【404 那一行保持 <p>】它是给眼睛看的状态码，不是页面标题；把它也做成标题
 * 会让大纲里出现"404"和"这个页面不存在"两个同级标题，反而是噪声。
 */
export default function NotFoundPage() {
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <p className="text-muted-foreground text-sm">404</p>
      <h1 className="mt-2 font-medium">这个页面不存在</h1>
      <Button asChild variant="outline" className="mt-4">
        <Link to="/knowledge-bases">回到知识库</Link>
      </Button>
    </div>
  )
}
