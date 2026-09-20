import { MoreHorizontal, Pencil, Trash2 } from 'lucide-react'
import { useNavigate } from 'react-router'

import type { Schemas } from '@congorag/api-client'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { formatDateTime } from '@/lib/format'

type KnowledgeBase = Schemas['KnowledgeBase']

type Props = {
  kb: KnowledgeBase
  onRename: (kb: KnowledgeBase) => void
  onDelete: (kb: KnowledgeBase) => void
}

/**
 * 知识库卡片。
 *
 * 操作入口（···）只在 hover 或聚焦时出现，和 Dify 数据集列表一致。
 * 用 group-hover 而不是 JS 状态：纯 CSS，不用为每张卡片存一份 hover 状态。
 *
 * 整卡可点击进详情页（文档列表）——用 onClick + useNavigate 而不是把
 * 整卡包一层 <Link>：卡片内部还有一个下拉菜单按钮，<Link> 包住按钮会
 * 产生嵌套的可交互元素，语义上不对（a 标签套 button）；onClick 挂在
 * 外层 div 上不会有这个问题，点菜单按钮时靠 stopPropagation 挡住冒泡。
 */
export function KnowledgeCard({ kb, onRename, onDelete }: Props) {
  const navigate = useNavigate()

  return (
    <div
      role="link"
      tabIndex={0}
      onClick={() => navigate(`/knowledge-bases/${kb.id}`)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') navigate(`/knowledge-bases/${kb.id}`)
      }}
      className="group border-border bg-card hover:bg-accent/40 relative flex h-40 cursor-pointer flex-col justify-between rounded-xl border p-4 transition-colors"
    >
      <div className="flex items-start gap-3">
        <span className="bg-primary/10 flex size-10 shrink-0 items-center justify-center rounded-lg text-lg">
          📙
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate font-medium" title={kb.name}>
            {kb.name}
          </div>
          <div className="text-muted-foreground mt-1 text-xs">
            更新于 {formatDateTime(kb.updatedAt)}
          </div>
        </div>
      </div>

      <div className="text-muted-foreground text-xs">
        创建于 {formatDateTime(kb.createdAt)}
      </div>

      {/* 操作菜单：默认不可见且不可点，hover / 键盘聚焦时才浮出。
          onClick 阻止冒泡，将来整卡可点击时不会误触发。 */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={`${kb.name} 的操作`}
            className="absolute top-2 right-2 size-8 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
            onClick={(e) => e.stopPropagation()}
          >
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-40">
          <DropdownMenuItem onSelect={() => onRename(kb)}>
            <Pencil />
            编辑
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          {/* variant="destructive" 是 shadcn 对删除类操作的约定 */}
          <DropdownMenuItem variant="destructive" onSelect={() => onDelete(kb)}>
            <Trash2 />
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
