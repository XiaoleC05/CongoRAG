import { BarChart3 } from 'lucide-react'
import { useState } from 'react'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { dayToRFC3339, useUsage } from '@/hooks/useUsage'

/**
 * 用量页（issue #76）。
 *
 * 【为什么不引图表库】前端引用方案 §2.5 明确写了"按模型聚合的 token_usage
 * 数字卡片即可，**不引入图表库**"。这条不是审美偏好：这个项目是本地优先的
 * 单机工具，一张表 + 三个数字已经能回答"我用掉了多少"，为此拉进一个
 * 图表库（以及它自己的主题、动画、可访问性补丁）是净亏——而且图表对读屏
 * 用户比表格更难用，表格是浏览器原生可访问的。
 *
 * 【它只有按模型这一维】`GET /api/v1/usage` 只做按模型聚合，不做按会话
 * 聚合——后者被明确判定过不做（要 JOIN `messages`，而只有聊天主路径有
 * `message_id`，按会话加起来会天然少于总量）。**不要顺手把按会话那一列
 * 补上**：补不出来，硬凑的数字是错的。
 */
export default function UsagePage() {
  // 【为什么存的是日期字符串，而不是 Date 对象】`<input type="date">` 的
  // value 天生就是 `YYYY-MM-DD`。先转成 Date 再存，就得在每次 onChange 时
  // 把用户还没打完的中间态（"2026-0"）丢掉——那会让输入框变得没法用。
  // 转换只发生在这两个字符串变成请求参数的那一刻（dayToRFC3339）。
  const [sinceDay, setSinceDay] = useState('')
  const [untilDay, setUntilDay] = useState('')

  // until 传 end = true：契约里它是**不含**的，取次日零点才能把用户选的
  // 那一天整天框进来。取当日 23:59:59 会漏掉最后一秒里的调用。
  const since = dayToRFC3339(sinceDay)
  const until = dayToRFC3339(untilDay, true)

  const { data, isPending, error } = useUsage({ since, until })

  const filtered = since !== undefined || until !== undefined

  return (
    <div className="mx-auto max-w-5xl p-6">
      <header className="mb-6">
        <h1 className="text-xl font-semibold">用量</h1>
        <p className="text-muted-foreground mt-1 text-sm">
          每次调用模型都会记一笔 token 用量，这里按模型汇总。你自己配的 Key、
          自己付的账，所以这些数字直接就是成本。
        </p>
      </header>

      {/* 时间窗。**筛选条件不折叠**：这一页只有这一组条件，藏进"高级筛选"
          只会让用户以为页面上没有筛选。 */}
      <section className="mb-6">
        <h2 className="sr-only">时间窗</h2>
        <div className="flex flex-wrap items-end gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="usage-since">起始日期</Label>
            <Input
              id="usage-since"
              type="date"
              className="w-40"
              value={sinceDay}
              onChange={(e) => setSinceDay(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="usage-until">结束日期（含当天）</Label>
            <Input
              id="usage-until"
              type="date"
              className="w-40"
              value={untilDay}
              onChange={(e) => setUntilDay(e.target.value)}
            />
          </div>
          {/* 没有条件可清时整个不渲染，而不是渲染成禁用——禁用的按钮
              会让人以为"等一下就能点"，而这里永远不会。 */}
          {filtered && (
            <Button
              variant="ghost"
              onClick={() => {
                setSinceDay('')
                setUntilDay('')
              }}
            >
              清除筛选
            </Button>
          )}
        </div>
      </section>

      {isPending ? (
        <SkeletonSummary />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : data.byModel.length === 0 ? (
        // 【两种空态，不是一种（§19）】"这段时间里没有"和"从来就没有"对用户
        // 意味着完全不同的下一步：前者要放宽时间窗，后者要先去对话几次。
        // 合并成一句"暂无数据"等于把出口也一起藏了。
        <EmptyState filtered={filtered} onClear={() => {
          setSinceDay('')
          setUntilDay('')
        }} />
      ) : (
        <>
          <section className="mb-6">
            <h2 className="sr-only">合计</h2>
            <div className="grid gap-3 sm:grid-cols-3">
              <StatCard label="调用次数" value={sumCalls(data.byModel)} />
              <StatCard label="输入 token" value={data.totalPromptTokens} />
              <StatCard label="输出 token" value={data.totalCompletionTokens} />
            </div>
          </section>

          <section>
            <h2 className="mb-3 text-base font-semibold">按模型</h2>
            <div className="border-border overflow-hidden rounded-xl border">
              <table className="w-full text-sm">
                <thead className="bg-muted/50 text-muted-foreground text-left">
                  <tr>
                    <th className="px-4 py-2 font-medium">模型</th>
                    <th className="px-4 py-2 font-medium">类型</th>
                    <th className="px-4 py-2 text-right font-medium">调用次数</th>
                    <th className="px-4 py-2 text-right font-medium">输入 token</th>
                    <th className="px-4 py-2 text-right font-medium">输出 token</th>
                  </tr>
                </thead>
                <tbody>
                  {data.byModel.map((row) => (
                    // 【key 用 modelId（uuid）而不是 modelName】两个 provider
                    // 可以配同名的模型（甚至同一个 provider 里重复配过），
                    // 用名字做 key 会撞——React 撞 key 的表现是漏渲染或串行，
                    // 而且只在特定数据下出现。modelId 是 llm_models 的主键。
                    <tr key={row.modelId} className="border-border border-t">
                      <td className="px-4 py-2 font-medium">{row.modelName}</td>
                      <td className="px-4 py-2">
                        {/* 给用户看的是中文而不是 chat / embedding——这一列
                            是给眼睛看的，契约里的枚举值不是给用户读的。 */}
                        <Badge variant="outline">
                          {row.kind === 'embedding' ? '向量化' : '对话'}
                        </Badge>
                      </td>
                      <td className="text-muted-foreground px-4 py-2 text-right">
                        {formatCount(row.calls)}
                      </td>
                      <td className="text-muted-foreground px-4 py-2 text-right">
                        {formatCount(row.promptTokens)}
                      </td>
                      <td className="text-muted-foreground px-4 py-2 text-right">
                        {formatCount(row.completionTokens)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {/* 【说清"调用次数会多于你以为的次数"】契约里写明了：哪怕上游
                没回 usage 也会计一次（那一行的 token 记 0）。不说的话，
                用户看到"调用 5 次、token 0"会以为记账坏了。 */}
            <p className="text-muted-foreground mt-2 text-xs">
              调用次数按请求条数记：上游没有返回用量信息的那次也会计一次，
              它的 token 计 0。
            </p>
          </section>
        </>
      )}
    </div>
  )
}

/**
 * 调用次数的合计。
 *
 * 【为什么在客户端加，而不是后端给一个 totalCalls】契约里只给了
 * totalPromptTokens / totalCompletionTokens 两个总量，没有调用次数总量。
 * 这里加的是同一份响应里的数字，不产生新的口径差异。**不要**因为它就顺手
 * 加一个"按会话"的维度——那条被明确判定过不做（见文件头的注释）。
 */
function sumCalls(rows: { calls: number }[]): number {
  return rows.reduce((acc, row) => acc + row.calls, 0)
}

/**
 * 千位分隔。手写而不是用 `Intl` / `toLocaleString`：本地工具站不需要
 * 本地化，而 `toLocaleString` 的输出跟着浏览器的 locale 走——同一个数字
 * 在两台机器上可能长得不一样，截图和 issue 里的数字就对不上了。
 */
function formatCount(n: number): string {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

function StatCard({ label, value }: { label: string; value: number }) {
  return (
    <div className="bg-card border-border rounded-xl border p-4">
      <p className="text-muted-foreground text-xs">{label}</p>
      {/* 数字不是标题，用 <p> 而不是标题标签：它不进大纲。 */}
      <p className="mt-1 text-2xl font-semibold tabular-nums">{formatCount(value)}</p>
    </div>
  )
}

function SkeletonSummary() {
  return (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-3">
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className="h-20 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-40 rounded-xl" />
    </div>
  )
}

/**
 * 空态。两种（见页面主体里的注释）：
 *  - 有时间窗 → 这段时间里没有记录，出口是"清除筛选"；
 *  - 没有时间窗 → 这个库里一次调用都没有，出口是去对话。
 */
function EmptyState({ filtered, onClear }: { filtered: boolean; onClear: () => void }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <BarChart3 className="text-muted-foreground mb-3 size-8" />
      {filtered ? (
        <>
          <p className="font-medium">这段时间里没有调用记录</p>
          <p className="text-muted-foreground mt-1 mb-4 text-sm">
            换一个时间范围试试，或者清掉筛选看全部。
          </p>
          <Button variant="outline" onClick={onClear}>
            清除筛选
          </Button>
        </>
      ) : (
        <>
          <p className="font-medium">还没有任何调用记录</p>
          <p className="text-muted-foreground mt-1 text-sm">
            去对话页或者 Agent 页跑一次，用量就会出现在这里。
          </p>
        </>
      )}
    </div>
  )
}
