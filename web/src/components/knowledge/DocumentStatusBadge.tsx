import { CheckCircle2, CircleAlert, Clock, Loader2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import type { Schemas } from '@congorag/api-client'

type Status = Schemas['Document']['status']

/**
 * 文档处理状态的徽标。四态对应 `internal/knowledge/document.go` 的状态机——
 * 这里只负责怎么显示，不做任何状态判断（是否可重试之类的逻辑不在前端）。
 *
 * 【四态必须"一眼分得开"，不能只换文字】issue #82 的验收标准。
 * 每一态同时用三个通道表达，任何一个失效都还认得出：
 *
 *   | 状态 | 颜色 | 图标 | 文字 |
 *   | queued     | 中性（无彩色） | Clock 静止 | 排队中 |
 *   | processing | 蓝（sky）      | Loader2 旋转 | 处理中 |
 *   | ready      | 绿（emerald）  | CheckCircle2 | 就绪 |
 *   | failed     | 红（destructive）| CircleAlert | 失败 |
 *
 * 【为什么 queued 和 processing 不能都只是 secondary】它们原本是同一个
 * variant，只差图标和文字——在"减弱动态效果"下 Loader2 停转之后，两者就
 * 只剩文字不同了。而这两态的含义差别很大（一个还没轮到、一个正在跑），
 * 颜色是最快的那条通道（web/README.md §13/§18：状态不能只靠一种呈现方式）。
 *
 * 【为什么用调色板类而不是新加语义 token】这里是"状态的语义色"，
 * 只有这一处用；为它往 index.css 加四个变量反而把主题令牌表稀释了。
 * 调色板类都带 `dark:` 变体，深浅两套主题下都成立——README 的"已达标项"
 * 里对颜色字面量的禁令说的是 `#hex` / `rgb()`，不是这一类。
 *
 * 【processing 必须有"还在动"的指示】§18：只给一个图标等于没说。
 * `animate-spin` 由 index.css 里那条全局的 `prefers-reduced-motion` 压停，
 * 压停之后文字"处理中"还在——状态静止可辨是 §21 的要求，不是漏掉了动画。
 */
export function DocumentStatusBadge({ status }: { status: Status }) {
  switch (status) {
    case 'queued':
      return (
        <Badge variant="outline">
          <Clock className="text-muted-foreground" />
          排队中
        </Badge>
      )
    case 'processing':
      return (
        <Badge variant="outline" className="text-sky-600 dark:text-sky-400">
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
