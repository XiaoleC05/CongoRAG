import { useQuery } from '@tanstack/react-query'

import { api } from '@congorag/api-client'

/**
 * 用量查询的时间窗。
 *
 * 【语义照抄契约，不自己发明】`since` 是「含」、`until` 是「不含」。
 * 两边都省略 = 不限（后端就是 `WHERE created_at >= $1 AND created_at < $2`
 * 的两个可选条件）。写成半个开区间是有意的：只有这样才能把相邻两段
 * 时间窗拼起来而不重不漏——闭区间拼两次会重复计算边界那一毫秒。
 */
export type UsageWindow = {
  since?: string
  until?: string
}

/**
 * 用量缓存的**前缀**。
 *
 * 【为什么要单独导出一个前缀】删掉一个模型时，它的 token_usage 行会跟着
 * 被级联删掉（`token_usage.model_id` 的 FK 是 ON DELETE CASCADE，契约里
 * `deleteModel` 的 description 点明了这件事）——也就是说**这一页的数字会
 * 因为别处的操作而过期**。要让那个操作能作废这里，它需要一个稳定的前缀
 * （`invalidateQueries` 是按前缀匹配的）。写成 `usageKey({})` 不行：
 * 那是 `['usage', null, null]`，匹配不到 `['usage', since, until]`。
 */
export const USAGE_KEY = ['usage']

/**
 * 用量汇总在缓存里的 key。
 *
 * 【为什么把时间窗编进 key】它不是一个"视图开关"，而是**请求参数**：
 * 不同的时间窗会打到不同的 SQL 上、拿到不同的结果。共用一个 key 的话，
 * 改一次时间窗要么看到上一个窗口的旧数据，要么需要手动 refetch——
 * 前者是那种不报错、只是数字不对的坏法（本该最难发现的一种）。
 *
 * 【为什么把 undefined 归一成 null】TanStack Query 算 key 的哈希时用
 * 结构化比较，undefined 和 null 是**不同**的值。归一之后，"没选起始
 * 时间"在所有调用点上只会产生一个 key，不会因为写法不同裂成两份缓存。
 */
export const usageKey = (window: UsageWindow) => [
  ...USAGE_KEY,
  window.since ?? null,
  window.until ?? null,
]

/**
 * 把日期输入框的 `YYYY-MM-DD` 换算成契约要的 RFC3339 时间串。
 *
 * 【为什么不用 `new Date('2026-09-01')`】这种只有日期的字符串会被 JS
 * 按 **UTC** 解析，于是"9 月 1 日"在东八区变成了 8 月 31 日 08:00——
 * 用户选的日期和实际查的区间差了一天，而且不报错。`new Date(y, m, d)`
 * 走的是本地时间，正好是用户点那个日期时心里的那个瞬间。
 *
 * `end = true` 时返回的是**次日零点**（不是当日 23:59:59）：因为契约里
 * `until` 是不含的，用次日零点才能把最后一天整天都框进来，而且不必
 * 去猜"23:59:59.999 够不够"。
 *
 * 【这个函数为什么放在 hooks 里，不放在 lib/format.ts】
 * 它只服务于用量页这一个调用点，而 lib/format.ts 不在本批次允许改动的
 * 文件清单里。第二个调用方出现时再搬过去。
 */
export function dayToRFC3339(day: string, end = false): string | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day)
  if (!m) return undefined // 输入框清空时给的是空串，这里顺带把它变成"不限"

  const [, y, mo, d] = m
  // 月是 0 基的。end 用"次日零点"而不是"同日 23:59:59"——见上面的注释。
  const dt = new Date(Number(y), Number(mo) - 1, Number(d) + (end ? 1 : 0))
  return Number.isNaN(dt.getTime()) ? undefined : dt.toISOString()
}

/**
 * 读：按模型聚合的 token 用量（`GET /api/v1/usage`，v3.0 交付）。
 *
 * 【它只做按模型聚合，不做按会话聚合】CHANGELOG 与契约的注释里都记了
 * 这一条：按会话聚合要 JOIN `messages`，而只有聊天主路径会填 `message_id`
 * （摘要压缩、记忆抽取、文档索引都说明不了归属），按会话加起来会天然少于
 * 总量。这个不一致几乎必然被用户发现，所以被明确判定过不做——**不要因为
 * 界面上少一列就顺手把按会话那条补上**。
 */
export function useUsage(window: UsageWindow) {
  return useQuery({
    queryKey: usageKey(window),
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/usage', {
        params: { query: { since: window.since, until: window.until } },
      })
      // openapi-fetch 用返回值而不是异常表示失败，转成 throw 交给 TanStack Query
      if (error) throw error
      return data
    },
  })
}
