import type { InfiniteData } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import { flattenPages, flattenPagesChronologically, nextPageParam, type Page } from './pagination'

/**
 * 分页摊平（issue #45）。
 *
 * 【这一组钉的是一个真的上过线的 bug】会话消息的分页方向是"第一页给最新的
 * N 条、翻下一页拿更早的"，所以 pages[0] 最新、pages[1] 更旧。用普通的
 * flatMap 摊平会让更早的消息排在最新消息的**下面**——整段聊天记录的时间
 * 顺序反过来，而且不报错、不崩、类型也对。
 */

function pages<T>(...items: T[][]): InfiniteData<Page<T>, string | null> {
  return {
    pages: items.map((i) => ({ items: i, nextCursor: null })),
    pageParams: items.map(() => null),
  }
}

describe('flattenPages', () => {
  it('按加载顺序摊平（用于"越往后越旧"的列表）', () => {
    expect(flattenPages(pages(['a', 'b'], ['c', 'd']))).toEqual(['a', 'b', 'c', 'd'])
  })

  it('没有数据时返回空数组而不是崩', () => {
    expect(flattenPages(undefined)).toEqual([])
  })
})

describe('flattenPagesChronologically', () => {
  it('把"越往后越旧"的分页翻成时间正序', () => {
    // pages[0] 是最新的一页（页内升序），pages[1] 是更早的一页。
    expect(flattenPagesChronologically(pages(['m3', 'm4'], ['m1', 'm2']))).toEqual([
      'm1',
      'm2',
      'm3',
      'm4',
    ])
  })

  it('【回归】直接 flatMap 会得到反过来的时间顺序', () => {
    const data = pages(['m3', 'm4'], ['m1', 'm2'])

    // 这两条断言是同一个数据上的两种读法——它们的差就是那个 bug。
    expect(flattenPages(data)).toEqual(['m3', 'm4', 'm1', 'm2'])
    expect(flattenPagesChronologically(data)).toEqual(['m1', 'm2', 'm3', 'm4'])
  })

  it('不修改缓存里的 pages 数组', () => {
    const data = pages(['m3'], ['m1'])
    const before = data.pages.map((p) => p.items)

    flattenPagesChronologically(data)

    // reverse() 是原地操作——忘了 slice() 就会把 query 的缓存本身改掉。
    expect(data.pages.map((p) => p.items)).toEqual(before)
  })

  it('只有一页时与普通摊平一致', () => {
    expect(flattenPagesChronologically(pages(['a', 'b']))).toEqual(['a', 'b'])
  })

  it('没有数据时返回空数组', () => {
    expect(flattenPagesChronologically(undefined)).toEqual([])
  })
})

describe('nextPageParam', () => {
  it('nextCursor 为 null 时没有下一页', () => {
    expect(nextPageParam({ items: [], nextCursor: null })).toBeUndefined()
  })

  it('nextCursor 有值时把它当下一页的参数', () => {
    expect(nextPageParam({ items: [], nextCursor: 'CUR' })).toBe('CUR')
  })
})
