import { MoreHorizontal, RotateCw, Trash2 } from 'lucide-react'

import type { Schemas } from '@congorag/api-client'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

type Document_ = Schemas['Document']

type Props = {
  doc: Document_
  onReindex: (doc: Document_) => void
  onDelete: (doc: Document_) => void
  pending: boolean
}

/**
 * 文档行尾的操作菜单：重新索引 / 删除。
 *
 * 【为什么收进溢出菜单】两个原因，缺一个都还能忍，合起来就不能忍：
 *   1. 加删除入口之后，行尾会是三个按钮（重新索引、删除、将来还会有更多），
 *      web/README.md §19 明确反对"每行堆一排按钮"；
 *   2. 文档行本身是表格的一格，按钮越多，文件名被挤得越窄——文件名才是
 *      这一行的主体。
 * 形态照 KnowledgeCard：一个 `···`，里面是全部次要操作。破坏性那一项用
 * `variant="destructive"`（shadcn 对删除类操作的约定），并用分隔线隔开。
 *
 * 【为什么不像 KnowledgeCard 那样 hover 才出现】表格行没有"卡片那么大"的
 * 悬停区，而且悬停态在触屏上没有等价物（§15）。它一直在，代价只是每行多
 * 一个 24px 的按钮。
 *
 * 【可访问名带上文件名】§14：一串"删除"里必须能听出是哪一行，所以
 * 触发器的 aria-label 是 `<文件名> 的操作` 而不是"操作"。菜单展开之后
 * 焦点在菜单里，对象由"是谁打开的"确定，菜单项文案保持和 KnowledgeCard
 * 一致（"删除"而不是"删除 notes.md"——菜单项里重复文件名反而不好读）。
 */
export function DocumentRowActions({ doc, onReindex, onDelete, pending }: Props) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={`${doc.filename} 的操作`} disabled={pending}>
          <MoreHorizontal className="text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-40">
        {/* 【单文档重新索引为什么必要】换 embedding 模型要重跑全库，但一份
            文档处理中途失败、或者被 32 MiB 上限 / 30 分钟超时卡住时，只需要
            重跑它自己。两个粒度是不同的用途，不是同一个按钮的两种说法。 */}
        <DropdownMenuItem onSelect={() => onReindex(doc)}>
          <RotateCw />
          重新索引
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={() => onDelete(doc)}>
          <Trash2 />
          删除
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
