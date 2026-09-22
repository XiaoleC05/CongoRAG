import { ArrowLeft, Bot } from 'lucide-react'
import { Link, useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { RunStatusBadge } from '@/components/agent/RunStatusBadge'
import { ToolCallCard } from '@/components/agent/ToolCallCard'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useRunSteps } from '@/hooks/useAgents'
import { formatDateTime } from '@/lib/format'

/**
 * 执行轨迹页——从数据库读回的 Step 记录,按 seq 升序,一步一卡。
 *
 * 【和 AgentDetailPage 的流式时间线是两个组件,故意不共用】那边是
 * SSE 事件驱动的本地状态（正在发生),这里是 GET /runs/{runId}/steps
 * 的查询结果（已经发生、落库的事实)——即使这次运行刚结束、内容看起来
 * 一样,数据来源不同,合并成一个组件会让"这个组件到底订阅的是什么"
 * 变得含糊。
 *
 * 【但工具卡片是同一张】工具名、参数、结果这三样在两个数据源里是同一批
 * 东西（agent_run_steps 的每一行都对应流里的一对 tool_call/tool_result），
 * 卡片长成两副样子只会让人以为是两种不同的记录。所以这里复用
 * ToolCallCard，只是把"跑完了没有"的判据从"有没有收到事件"换成 step.status
 * ——事后视图里 toolResult 是 null 与"还没返回"同形。
 */
export default function RunTracePage() {
  const { agentId, runId } = useParams<{ agentId: string; runId: string }>()
  const { data: steps, isPending, error } = useRunSteps(runId ?? '')

  return (
    <div className="mx-auto max-w-3xl p-6">
      <div className="mb-4">
        {/* 绝对路径,不用相对的 ".."——agents/:id 和 agents/:agentId/runs/:runId
            在路由树里是同级（都直接挂在 AppLayout 下),不是父子嵌套关系,
            相对路径在这里解析出来的不是想要的目标。 */}
        <Button variant="ghost" size="sm" asChild className="-ml-2">
          <Link to={`/agents/${agentId ?? ''}`}>
            <ArrowLeft />
            返回 Agent
          </Link>
        </Button>
      </div>

      <h1 className="mb-4 text-xl font-semibold">执行轨迹</h1>

      {isPending ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-16 rounded-lg" />
          ))}
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : steps.length === 0 ? (
        <p className="text-muted-foreground text-sm">这次运行还没有产生任何步骤。</p>
      ) : (
        <ol className="space-y-3">
          {steps.map((step) => (
            <li key={step.id} className="border-border rounded-lg border p-3">
              <div className="flex items-center gap-2">
                {step.type !== 'tool' && (
                  <Bot className="text-muted-foreground size-4 shrink-0" />
                )}
                <span className="text-muted-foreground text-xs">#{step.seq}</span>
                {step.type !== 'tool' && <span className="font-medium">模型生成</span>}
                {/* 状态徽章是共用的那一份映射（README §17）：文案与配色只有
                    一个定义处，不会和实时视图、历史列表各说各话。step 少了
                    cancelled 这一态（取消发生在 run 层），所以它只会出现其中
                    五种——表不因此分叉成两张。 */}
                <RunStatusBadge status={step.status} className="ml-auto" />
              </div>

              {/* 工具步骤用折叠卡片：默认收起，点开看参数与结果全文（issue #80）。
                  名字与图标都在卡片里，所以这里不再重复渲染工具名。 */}
              {step.type === 'tool' && (
                <div className="mt-2">
                  <ToolCallCard
                    name={step.toolName ?? ''}
                    args={step.toolArgs}
                    result={step.toolResult}
                    // 事后视图按 step.status 判状态（含 failed / interrupted，
                    // 以及"这一行的 tool_result 是空的"那件事），见卡片上
                    // stepStatus 的说明。
                    stepStatus={step.status}
                  />
                </div>
              )}

              {step.error && <p className="text-destructive mt-2 text-xs">{step.error}</p>}

              <div className="text-muted-foreground mt-2 text-xs">
                {step.latencyMs != null && <span>{step.latencyMs} ms · </span>}
                {formatDateTime(step.createdAt)}
              </div>
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}
