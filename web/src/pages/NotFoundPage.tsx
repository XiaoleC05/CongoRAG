import { Link } from 'react-router'

import { Button } from '@/components/ui/button'

/**
 * 前端路由没匹配上时的兜底页面。
 *
 * 注意：这只覆盖"进入了 React 但路由表里没有"的情况。
 * 服务端那条兜底在 apps/api/internal/api/spa.go 的 MountSPA——
 * 它让任何未匹配路径返回 index.html，前端才有机会渲染这一页。
 */
export default function NotFoundPage() {
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <p className="text-muted-foreground text-sm">404</p>
      <p className="mt-2 font-medium">这个页面不存在</p>
      <Button asChild variant="outline" className="mt-4">
        <Link to="/knowledge-bases">回到知识库</Link>
      </Button>
    </div>
  )
}
