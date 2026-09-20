import { ArrowLeft, Bot, Wrench } from 'lucide-react'
import { Link, useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useRunSteps } from '@/hooks/useAgents'
import { formatDateTime } from '@/lib/format'

const STATUS_VARIANT: Record<string, 'default' | 'destructive' | 'outline'> = {
  completed: 'default',
  failed: 'destructive',
  pending: 'outline',
  running: 'outline',
  interrupted: 'destructive',
}

/**
 * 执行轨迹页——从数据库读回的 Step 记录,按 seq 升序,一步一卡。
 *
 * 【和 AgentDetailPage 的流式时间线是两个组件,故意不共用】那边是
 * SSE 事件驱动的本地状态（正在发生),这里是 GET /runs/{runId}/steps
 * 的查询结果（已经发生、落库的事实)——即使这次运行刚结束、内容看起来
 * 一样,数据来源不同,合并成一个组件会让"这个组件到底订阅的是什么"
 * 变得含糊。
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
                {step.type === 'tool' ? (
                  <Wrench className="text-muted-foreground size-4 shrink-0" />
                ) : (
                  <Bot className="text-muted-foreground size-4 shrink-0" />
                )}
                <span className="text-muted-foreground text-xs">#{step.seq}</span>
                <span className="font-medium">
                  {step.type === 'tool' ? step.toolName : '模型生成'}
                </span>
                <Badge variant={STATUS_VARIANT[step.status] ?? 'outline'} className="ml-auto">
                  {step.status}
                </Badge>
              </div>

              {step.type === 'tool' && (
                <div className="mt-2 grid grid-cols-2 gap-2 text-xs">
                  <div>
                    <div className="text-muted-foreground mb-1">参数</div>
                    <pre className="bg-muted overflow-x-auto rounded-md p-2">
                      {JSON.stringify(step.toolArgs, null, 2)}
                    </pre>
                  </div>
                  <div>
                    <div className="text-muted-foreground mb-1">结果</div>
                    <pre className="bg-muted overflow-x-auto rounded-md p-2">
                      {JSON.stringify(step.toolResult, null, 2)}
                    </pre>
                  </div>
                </div>
              )}

              {step.error && (
                <p className="text-destructive mt-2 text-xs">{step.error}</p>
              )}

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
