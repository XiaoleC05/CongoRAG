import { CheckCircle2, CircleAlert, Clock, Loader2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import type { Schemas } from '@congorag/api-client'

type Status = Schemas['Document']['status']

/**
 * 文档处理状态的徽标。四态对应 `internal/knowledge/document.go` 的状态机——
 * 这里只负责怎么显示，不做任何状态判断（是否可重试之类的逻辑不在前端）。
 */
export function DocumentStatusBadge({ status }: { status: Status }) {
  switch (status) {
    case 'queued':
      return (
        <Badge variant="secondary">
          <Clock />
          排队中
        </Badge>
      )
    case 'processing':
      return (
        <Badge variant="secondary">
          <Loader2 className="animate-spin" />
          处理中
        </Badge>
      )
    case 'ready':
      return (
        <Badge variant="outline" className="text-emerald-600 dark:text-emerald-400">
          <CheckCircle2 />
          就绪
        </Badge>
      )
    case 'failed':
      return (
        <Badge variant="destructive">
          <CircleAlert />
          失败
        </Badge>
      )
  }
}
