import type { InfiniteData } from '@tanstack/react-query'

/**
 * 分页响应的形状（issue #45）。三个会无界增长的列表都用它。
 *
 * nextCursor 为 null / 缺省表示「到底了」——据此把「加载更多」收起来，
 * 而不是让它一直亮着、点了没反应。
 */
export type Page<T> = {
  items: T[]
  nextCursor?: string | null
}

/**
 * 把 useInfiniteQuery 的 pages 摊平成一维数组。
 *
 * 【为什么要有这个函数】分页之前这三个列表都是 `T[]`，页面里到处是
 * `data.map(...)`。改成无限查询之后缓存形状是 `{pages, pageParams}`，
 * 每一页各自是 `{items, nextCursor}`——摊平这件事在三个页面里写法完全一样，
 * 写三遍迟早会漏掉一处（漏掉的那处会静默渲染成空列表）。
 */
export function flattenPages<T>(data: InfiniteData<Page<T>> | undefined): T[] {
  if (!data) return []
  return data.pages.flatMap((page) => page.items)
}

/**
 * 「还有下一页吗」。
 *
 * useInfiniteQuery 的 hasNextPage 已经能回答这个问题，这个包装只是把
 * `getNextPageParam` 的判据集中到一处，免得三个 hook 各写一遍
 * `last.nextCursor ?? undefined`。
 */
export function nextPageParam(last: Page<unknown>): string | undefined {
  return last.nextCursor ?? undefined
}
