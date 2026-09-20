import { FileText } from 'lucide-react'

import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from '@/components/ui/hover-card'
import type { DisplayCitation } from '@/hooks/useMessages'

/**
 * Perplexity 式引用角标：正文里的 [1] [2]，悬停展开来源卡片。
 * 前端引用方案 §2.2 点名的交互——citation 是一等公民,随流下发,
 * 不是等答案生成完才给（这个属性由后端保证,这里只负责展示）。
 */
export function CitationBadge({ index, citation }: { index: number; citation: DisplayCitation }) {
  return (
    <HoverCard openDelay={100}>
      <HoverCardTrigger asChild>
        <sup className="text-primary mx-0.5 cursor-pointer font-medium hover:underline">
          [{index}]
        </sup>
      </HoverCardTrigger>
      <HoverCardContent className="w-80" side="top">
        <div className="flex items-start gap-2">
          <FileText className="text-muted-foreground mt-0.5 size-4 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">{citation.filename}</div>
            <p className="text-muted-foreground mt-1 line-clamp-4 text-xs whitespace-pre-wrap">
              {citation.snippet}
            </p>
            <div className="text-muted-foreground mt-1.5 text-xs">
              相似度 {(citation.score * 100).toFixed(0)}%
            </div>
          </div>
        </div>
      </HoverCardContent>
    </HoverCard>
  )
}
