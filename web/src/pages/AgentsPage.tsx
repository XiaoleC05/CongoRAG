import { Bot, Plus } from 'lucide-react'
import { useState } from 'react'
import { useNavigate } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { AgentFormDialog } from '@/components/agent/AgentFormDialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useAgents } from '@/hooks/useAgents'
import { formatDateTime } from '@/lib/format'

/** Agent 列表页——Dify 数据集列表同一个思路的卡片网格。 */
export default function AgentsPage() {
  const { data, isPending, error } = useAgents()
  const [creating, setCreating] = useState(false)
  // 每次打开表单都递增，用作它的 key：换 key 让 React 重新挂载组件，
  // 输入框拿到新的初始值、上一次的报错也不会跟过来（§7/§8，比在弹窗里用
  // effect 监听 open 干净）。
  const [formSeq, setFormSeq] = useState(0)
  const navigate = useNavigate()

  const openCreate = () => {
    setCreating(true)
    setFormSeq((n) => n + 1)
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      <header className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Agent</h1>
        <Button onClick={openCreate}>
          <Plus />
          创建
        </Button>
      </header>

      {isPending ? (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-32 rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : data.length === 0 ? (
        <EmptyState onCreate={openCreate} />
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3">
          {data.map((agent) => (
            <div
              key={agent.id}
              role="link"
              tabIndex={0}
              onClick={() => navigate(`/agents/${agent.id}`)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') navigate(`/agents/${agent.id}`)
              }}
              className="border-border bg-card hover:bg-accent/40 flex h-32 cursor-pointer flex-col justify-between rounded-xl border p-4 transition-colors"
            >
              <div className="flex items-start gap-3">
                <span className="bg-primary/10 flex size-10 shrink-0 items-center justify-center rounded-lg">
                  <Bot className="size-5" />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium" title={agent.name}>
                    {agent.name}
                  </div>
                  <div className="text-muted-foreground mt-1 truncate text-xs">
                    {agent.description || '没有描述'}
                  </div>
                </div>
              </div>
              <div className="text-muted-foreground text-xs">
                创建于 {formatDateTime(agent.createdAt)}
              </div>
            </div>
          ))}
        </div>
      )}

      <AgentFormDialog key={`create-${formSeq}`} open={creating} onOpenChange={setCreating} />
    </div>
  )
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <Bot className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">还没有 Agent</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        建一个，给它几个工具，就能让它自己决定怎么完成任务。
      </p>
      <Button onClick={onCreate}>
        <Plus />
        创建第一个 Agent
      </Button>
    </div>
  )
}
