import { ArrowLeft, Send } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { ToolCallCard } from '@/components/agent/ToolCallCard'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { useAgent, useAgentRuns } from '@/hooks/useAgents'
import { useStartAgentRun } from '@/hooks/useStartAgentRun'
import { flattenPages } from '@/lib/pagination'
import { formatDateTime } from '@/lib/format'

const STATUS_LABEL: Record<string, string> = {
  pending: '排队中',
  running: '运行中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '已中断',
}

/**
 * Agent 详情页：运行输入框 + 当前这次运行的流式时间线 + 历史运行列表。
 *
 * 【流式时间线和执行轨迹页是两个不同的东西】这个页面展示的是"正在
 * 发生"的过程（本地状态,SSE 事件驱动),历史运行列表里每一项点进去
 * 看到的是 RunTracePage——从数据库读回来的、已经落库的 Step 记录。
 * 两者故意不合并成一个组件：当前运行结束后,历史列表会通过 query
 * 失效自动出现这次运行,用户想回看时走的是同一条"查历史"路径,
 * 不需要为"这次刚跑完的" 特殊处理。
 */
export default function AgentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const agentId = id ?? ''

  const { data: agent, isPending, error } = useAgent(agentId)
  const { data: runPages, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useAgentRuns(agentId)
  const runs = flattenPages(runPages)
  const { start, isRunning, timeline, runError } = useStartAgentRun(agentId)
  const navigate = useNavigate()

  const [input, setInput] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

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
            <h1 className="text-xl font-semibold">{agent.name}</h1>
            {agent.description && (
              <p className="text-muted-foreground mt-1 text-sm">{agent.description}</p>
            )}
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
              {timeline.map((item, i) =>
                item.kind === 'text' ? (
                  <div key={i} className="bg-muted rounded-2xl px-4 py-2.5 text-sm whitespace-pre-wrap">
                    {item.content}
                    {isRunning && i === timeline.length - 1 && (
                      <span className="ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-current align-text-bottom" />
                    )}
                  </div>
                ) : (
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
            <Button type="submit" disabled={isRunning || !input.trim()}>
              <Send />
            </Button>
          </form>

          {!!runs.length && (
            <div className="mt-6">
              <h2 className="text-muted-foreground mb-2 text-sm font-medium">历史运行</h2>
              <div className="divide-border divide-y rounded-lg border">
                {runs.map((run) => (
                  <button
                    key={run.id}
                    type="button"
                    onClick={() => navigate(`/agents/${agentId}/runs/${run.id}`)}
                    className="hover:bg-accent/40 flex w-full items-center justify-between px-3 py-2 text-left text-sm"
                  >
                    <span className="min-w-0 flex-1 truncate">{run.input}</span>
                    <span className="text-muted-foreground ml-3 shrink-0 text-xs">
                      {STATUS_LABEL[run.status] ?? run.status} · {formatDateTime(run.createdAt)}
                    </span>
                  </button>
                ))}
              </div>
              {/* 运行历史按 created_at 倒序，更旧的在下面。 */}
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
            </div>
          )}
        </>
      )}
    </div>
  )
}
