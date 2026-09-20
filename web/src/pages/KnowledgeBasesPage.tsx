import { BookOpen, Plus } from 'lucide-react'
import { useState } from 'react'

import type { Schemas } from '@congorag/api-client'

import { DeleteKnowledgeDialog } from '@/components/knowledge/DeleteKnowledgeDialog'
import { KnowledgeCard } from '@/components/knowledge/KnowledgeCard'
import { NameDialog } from '@/components/knowledge/NameDialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  useKnowledgeBaseMutations,
  useKnowledgeBases,
} from '@/hooks/useKnowledgeBases'
import { ErrorText } from '@/components/ErrorText'

type KnowledgeBase = Schemas['KnowledgeBase']

/** 卡片网格的列宽。和 Dify 数据集列表同一个思路：自适应列数，最小 280px。 */
const GRID = 'grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3'

export default function KnowledgeBasesPage() {
  const { data, isPending, error } = useKnowledgeBases()
  const { create, rename, remove } = useKnowledgeBaseMutations()

  // 三个弹窗的开关状态都在页面这一层，卡片和按钮只负责"请求打开"。
  // 让卡片自己持有弹窗状态的话，一张卡一个实例，删完之后状态就乱了。
  const [creating, setCreating] = useState(false)
  const [renaming, setRenaming] = useState<KnowledgeBase | null>(null)
  const [deleting, setDeleting] = useState<KnowledgeBase | null>(null)

  // 每次打开表单弹窗都递增，用作它的 key。
  // 换 key 会让 React 重新挂载组件，输入框因此拿到新的初始值——
  // 比在组件里用 effect 监听 open 再 setState 干净（少一次渲染，oxlint 也不报）。
  const [formSeq, setFormSeq] = useState(0)

  // 打开弹窗时先清掉上一次的错误，否则重开时会看到已经过期的报错。
  const openCreate = () => {
    create.reset()
    setCreating(true)
    setFormSeq((n) => n + 1)
  }
  const openRename = (kb: KnowledgeBase) => {
    rename.reset()
    setRenaming(kb)
    setFormSeq((n) => n + 1)
  }
  const openDelete = (kb: KnowledgeBase) => {
    remove.reset()
    setDeleting(kb)
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      <header className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold">知识库</h1>
        <Button onClick={openCreate}>
          <Plus />
          创建
        </Button>
      </header>

      {/* 三种状态都要处理：漏掉加载态会先闪一下空列表，
          漏掉错误态会得到一片空白，用户不知道发生了什么。 */}
      {isPending ? (
        <SkeletonGrid />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
            <p className="text-muted-foreground mt-2 text-sm">
              先确认后端服务起来了：<code>make dev</code>
            </p>
          </AlertDescription>
        </Alert>
      ) : data.length === 0 ? (
        <EmptyState onCreate={openCreate} />
      ) : (
        <div className={GRID}>
          {data.map((kb) => (
            <KnowledgeCard
              key={kb.id}
              kb={kb}
              onRename={openRename}
              onDelete={openDelete}
            />
          ))}
        </div>
      )}

      <NameDialog
        key={`create-${formSeq}`}
        open={creating}
        onOpenChange={setCreating}
        title="新建知识库"
        description="给它起个名字。上传文档是下一步的事。"
        submitLabel="创建"
        pending={create.isPending}
        error={create.error}
        onSubmit={(name) =>
          create.mutate(name, { onSuccess: () => setCreating(false) })
        }
      />

      <NameDialog
        key={`rename-${formSeq}`}
        open={!!renaming}
        onOpenChange={(open) => !open && setRenaming(null)}
        title="重命名知识库"
        submitLabel="保存"
        initialValue={renaming?.name ?? ''}
        pending={rename.isPending}
        error={rename.error}
        onSubmit={(name) => {
          if (!renaming) return
          rename.mutate(
            { id: renaming.id, name },
            { onSuccess: () => setRenaming(null) },
          )
        }}
      />

      <DeleteKnowledgeDialog
        target={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => {
          if (!deleting) return
          remove.mutate(deleting.id, { onSuccess: () => setDeleting(null) })
        }}
      />
    </div>
  )
}

/** 加载态用和真实卡片同高的骨架，避免数据到达时页面跳一下。 */
function SkeletonGrid() {
  return (
    <div className={GRID}>
      {Array.from({ length: 6 }, (_, i) => (
        <Skeleton key={i} className="h-40 rounded-xl" />
      ))}
    </div>
  )
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <BookOpen className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">还没有知识库</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        建一个，之后往里上传文档就能问答了。
      </p>
      <Button onClick={onCreate}>
        <Plus />
        创建第一个知识库
      </Button>
    </div>
  )
}
