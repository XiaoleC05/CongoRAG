import { ArrowLeft, Loader2, Pencil, Send, Square } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { AgentFormDialog } from '@/components/agent/AgentFormDialog'
import { RunListToolbar } from '@/components/agent/RunListToolbar'
import { RunStatusBadge } from '@/components/agent/RunStatusBadge'
import { ToolCallCard } from '@/components/agent/ToolCallCard'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { RUN_FILTER_LABEL, sortAndFilterRuns, useAgent, useAgentRuns } from '@/hooks/useAgents'
import type { RunFilter, RunSort } from '@/hooks/useAgents'
import { useStartAgentRun } from '@/hooks/useStartAgentRun'
import { flattenPages } from '@/lib/pagination'
import { formatDateTime } from '@/lib/format'

/**
 * Agent 详情页：运行输入框 + 当前这次运行的流式时间线 + 历史运行列表。
 *
 * 【流式时间线和执行轨迹页是两个不同的东西】这个页面展示的是"正在
 * 发生"的过程（本地状态,SSE 事件驱动),历史运行列表里每一项点进去
 * 看到的是 RunTracePage——从数据库读回来的、已经落库的 Step 记录。
 * 两者故意不合并成一个组件：当前运行结束后,历史列表会通过 query
 * 失效自动出现这次运行,用户想回看时走的是同一条"查历史"路径,
 * 不需要为"这次刚跑完的" 特殊处理。
 * （工具卡片本身是共用的——同一批步骤在两边都该长成同一张卡。）
 */
export default function AgentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const agentId = id ?? ''

  const { data: agent, isPending, error } = useAgent(agentId)
  const { data: runPages, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useAgentRuns(agentId)
  const runs = flattenPages(runPages)
  const { start, cancel, isRunning, isCancelling, timeline, runError, runId, status } =
    useStartAgentRun(agentId)
  const navigate = useNavigate()

  const [input, setInput] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  // 排序 / 筛选是**视图状态**，不是服务器状态（issue #92 的另一半）。
  // 它不进 query key：改了排序不该触发重新请求，理由见 useAgents.ts 的
  // sortAndFilterRuns（游标编码的是服务器顺序，和这里的排序无关）。
  const [sort, setSort] = useState<RunSort>('newest')
  const [filter, setFilter] = useState<RunFilter>('all')
  const visibleRuns = sortAndFilterRuns(runs, sort, filter)

  // 编辑表单的开关与它的 key。换 key 让弹窗重新挂载，输入框拿到这条 Agent
  // 的当前值（§8），连续打开两次也不会留上一次改到一半的内容。
  const [editing, setEditing] = useState(false)
  const [formSeq, setFormSeq] = useState(0)
  const openEdit = () => {
    setEditing(true)
    setFormSeq((n) => n + 1)
  }

  useEffect(() => {
    scrollRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [timeline])

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const text = input.trim()
    if (!text || isRunning) return
    setInput('')
    void start(text)
  }

  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col p-6">
      <div className="mb-4">
        <Button variant="ghost" size="sm" asChild className="-ml-2">
          <Link to="/agents">
            <ArrowLeft />
            返回 Agent 列表
          </Link>
        </Button>
      </div>

      {isPending ? (
        <div className="space-y-4">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-24 rounded-xl" />
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : (
        <>
          <header className="mb-4">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h1 className="text-xl font-semibold">{agent.name}</h1>
                {agent.description && (
                  <p className="text-muted-foreground mt-1 text-sm">{agent.description}</p>
                )}
              </div>
              {/* 配置入口（issue #81）。名称 / 系统提示词 / 工具集都在弹窗里改，
                  改完走 PATCH，失效 AGENTS_KEY 前缀 → 这个页面上的名字跟着变。 */}
              <Button variant="outline" size="sm" onClick={openEdit}>
                <Pencil />
                编辑
              </Button>
            </div>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {agent.toolNames.map((name) => (
                <Badge key={name} variant="outline">
                  {name}
                </Badge>
              ))}
            </div>
          </header>

          <ScrollArea className="flex-1">
            <div className="space-y-3 pr-4">
              {/* 本次运行的状态徽章。它只在这次页面会话里存在过运行时有意义
                  ——没跑过任何一次时不渲染，免得留一个空的"本次运行"标头。 */}
              {status !== null && (
                <div className="flex items-center gap-2 text-xs">
                  <span className="text-muted-foreground">本次运行</span>
                  <RunStatusBadge status={status} />
                </div>
              )}

              {timeline.map((item, i) =>
                item.kind === 'text' ? (
                  <div key={i} className="bg-muted rounded-2xl px-4 py-2.5 text-sm whitespace-pre-wrap">
                    {item.content}
                    {isRunning && i === timeline.length - 1 && (
                      <span className="ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-current align-text-bottom" />
                    )}
                  </div>
                ) : (
                  // 工具调用折叠卡片（issue #80）：工具名、参数、结果，默认收起。
                  <ToolCallCard key={i} name={item.name} args={item.args} result={item.result} />
                ),
              )}

              {runError !== null && (
                <Alert variant="destructive">
                  <AlertTitle>运行失败</AlertTitle>
                  <AlertDescription>
                    <ErrorText error={runError} />
                  </AlertDescription>
                </Alert>
              )}

              <div ref={scrollRef} />
            </div>
          </ScrollArea>

          <form onSubmit={handleSubmit} className="mt-4 flex gap-2">
            <Input
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="给这个 Agent 一个任务…"
              disabled={isRunning}
              autoFocus
            />
            {/* 【取消不是"停止看"，是"别跑了"】一次 run 会串起多步工具调用和
                多次模型调用，每一个 token 都在花用户自己配的额度，所以这里
                接的是后端的 cancel 端点，而不是像对话页那样只掐掉本地连接：
                本地掐断在服务端留下的是 interrupted（连接没了），用户按的是
                "我不要了"，那该是 cancelled。两者的区别后端也是显式做的。

                【拿不到 runId 时按钮是禁用的】runId 只来自流的首帧
                run_started。首帧还没到的那一帧里没有 id 可发，禁用比发一个
                假请求或静默失败都诚实——而这个窗口只有一帧。首帧永远不来
                只有一种情况：这次运行根本没开始（空输入、Agent 不存在、
                没有可用模型），那时也没有东西需要取消，页面上给的是错误
                提示而不是取消按钮。

                【图标按钮要有可访问名】README §14。 */}
            {isRunning ? (
              <Button
                type="button"
                variant="secondary"
                onClick={() => void cancel()}
                disabled={runId === null || isCancelling}
                aria-label="取消运行"
              >
                {isCancelling ? <Loader2 className="animate-spin" /> : <Square />}
              </Button>
            ) : (
              <Button type="submit" disabled={!input.trim()} aria-label="运行">
                <Send />
              </Button>
            )}
          </form>

          {/* 历史运行。三态 + 两种空态（§2/§13/§19）：
              - 一条都没跑过 → RunHistoryEmptyState（引导去下面的输入框）
              - 跑过、但筛完没匹配 → RunNoMatchState（出口是"清除筛选"）
              两个态是两件事，文案与出口都不一样，所以是两个组件。 */}
          <div className="mt-6">
            <h2 className="text-muted-foreground mb-2 text-sm font-medium">历史运行</h2>

            {runs.length === 0 ? (
              <RunHistoryEmptyState />
            ) : (
              <>
                <RunListToolbar
                  sort={sort}
                  filter={filter}
                  onSortChange={setSort}
                  onFilterChange={setFilter}
                  loadedCount={runs.length}
                />
                {visibleRuns.length === 0 ? (
                  <RunNoMatchState
                    filter={filter}
                    loadedCount={runs.length}
                    hasMore={!!hasNextPage}
                    onClear={() => setFilter('all')}
                  />
                ) : (
                  <div className="divide-border divide-y rounded-lg border">
                    {visibleRuns.map((run) => (
                      <button
                        key={run.id}
                        type="button"
                        onClick={() => navigate(`/agents/${agentId}/runs/${run.id}`)}
                        className="hover:bg-accent/40 flex w-full items-center justify-between px-3 py-2 text-left text-sm"
                      >
                        <span className="min-w-0 flex-1 truncate">{run.input}</span>
                        <span className="text-muted-foreground ml-3 flex shrink-0 items-center gap-2 text-xs">
                          {formatDateTime(run.createdAt)}
                          {/* 六态都在这里出现（含 v4.0 起真的会写入的 interrupted）。 */}
                          <RunStatusBadge status={run.status} />
                        </span>
                      </button>
                    ))}
                  </div>
                )}
                {/* 【加载更多放在列表下方】默认顺序是 created_at 倒序，更旧的在下面。
                    hasNextPage 为假时整个不渲染——禁用会让用户以为等一下就有了。
                    【筛完没结果时它也要在】没加载到的页不参与客户端的筛选，
                    所以要留一条"继续往后翻"的路，否则用户会以为这个 Agent
                    真的没跑过失败的运行。 */}
                {hasNextPage && (
                  <div className="mt-2 flex justify-center">
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={isFetchingNextPage}
                      onClick={() => void fetchNextPage()}
                    >
                      {isFetchingNextPage ? '加载中…' : '加载更多'}
                    </Button>
                  </div>
                )}
              </>
            )}
          </div>
        </>
      )}

      {/* 编辑表单（issue #81）。agent 还没加载回来时不渲染——它的初始值
          就是这条 Agent 的当前配置。 */}
      {agent && (
        <AgentFormDialog
          key={`edit-${formSeq}`}
          open={editing}
          onOpenChange={setEditing}
          agent={agent}
        />
      )}
    </div>
  )
}

/** 集合本来就是空的：这个 Agent 一次都没跑过。出口是页面上的输入框，不是按钮。 */
function RunHistoryEmptyState() {
  return (
    <div className="border-border text-muted-foreground rounded-lg border border-dashed px-3 py-6 text-center text-sm">
      还没有运行过。在上面的输入框里给它一个任务，跑完的记录会出现在这里。
    </div>
  )
}

/**
 * 筛完没有结果。**和上面的空态是两件事**（§13/§19）：这里跑过，只是当前条件下
 * 一条都不匹配。所以文案要说清"筛的是什么、在多少条里筛的"，出口是"清除筛选"。
 *
 * 【hasMore 的那句话不能省】筛选只作用于已经加载到客户端的页，更早的记录还没
 * 参与筛选。不说的话，用户会以为"这个 Agent 从没失败过"。
 */
function RunNoMatchState({
  filter,
  loadedCount,
  hasMore,
  onClear,
}: {
  filter: RunFilter
  loadedCount: number
  hasMore: boolean
  onClear: () => void
}) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-lg border border-dashed px-4 py-12 text-center">
      <p className="font-medium">没有「{RUN_FILTER_LABEL[filter]}」的运行</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        已加载的 {loadedCount} 条里没有符合这个条件的。
        {hasMore && '更早的记录还没加载，可以点下面的「加载更多」继续找。'}
      </p>
      <Button variant="outline" onClick={onClear}>
        清除筛选
      </Button>
    </div>
  )
}
