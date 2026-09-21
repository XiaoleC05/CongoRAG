import { Skeleton } from '@/components/ui/skeleton'

/**
 * 路由懒加载的兜底骨架（<Suspense fallback>）。
 *
 * 【为什么不是整屏居中的转圈】它渲染在 AppLayout 的 <main> 里面——那一刻
 * 侧栏、顶栏都已经挂载好了。整屏居中的 spinner 会浮在内容区正中，
 * 看起来像"界面崩了一半"，而不是"内容在加载"。所以照页面自己的排版铺一层骨架
 * （标题条 + 卡片网格），换页时只有内容区在变。
 *
 * 【它是第五种状态，不要和页面内部的 isPending 合并】
 * README §2 要求每个页面处理 isPending / error / 空态三种状态，
 * 加上这里这个是第四种之外的另一种"加载中"，两者含义不同：
 *   - 这里的 Suspense 兜底 = "这个页面的代码还没到"（chunk 还在下载）
 *   - 页面里的 isPending  = "代码已经到了，数据还没到"（请求在飞）
 * 合并成一个的话，第一次进一个没缓存的页面会先闪一次骨架、再闪一次骨架。
 * 更麻烦的是：这里的骨架只能猜排版，页面里的骨架是照着真实数据铺的——
 * 混在一起就再也分不清"慢的是网络还是打包产物"。
 *
 * 【role="status" + aria-busy】读屏软件会念一次加载状态；
 * 只放 aria-busy 的话是静默的，用户只会觉得"点了没反应"。
 * 可见的只有骨架，文字放 sr-only，避免在界面上出现"加载中…"这种废话。
 */
export function PageFallback() {
  return (
    <div
      className="mx-auto max-w-6xl p-6"
      data-testid="page-fallback"
      role="status"
      aria-busy="true"
    >
      <span className="sr-only">页面加载中</span>

      {/* 标题条占的位置和页面里的 <header> 一致，骨架换成实内容时不跳一下 */}
      <div className="mb-6 flex items-center justify-between">
        <Skeleton className="h-7 w-32" />
        <Skeleton className="h-8 w-20" />
      </div>

      {/* 卡片网格的列宽抄的是知识库列表/Agent 列表那两个页面，保持一致 */}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3">
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className="h-32 rounded-xl" />
        ))}
      </div>
    </div>
  )
}
