import { RUN_STATUS_LABEL, RUN_STATUS_VARIANT, type RunStatus } from '@/components/agent/runStatus'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

type Props = {
  status: RunStatus
  className?: string
}

/**
 * run / step 状态徽章——六态只有这一个渲染点（issue #80）。
 *
 * 【为什么值得单独一个组件】同一份状态在三个地方出现：Agent 详情页的
 * 实时运行、历史运行列表、执行轨迹页的每一步。写三遍首先漂的是文案，
 * 其次是配色，而且都不报错。映射表在 runStatus.ts 里，这里只管渲染。
 *
 * 【兜底不是多余的】契约的类型只在编译期成立，服务端返回一个没声明过的
 * 状态时索引出来是 undefined——那时渲染成空白徽章比渲染成原文更糟，
 * 所以两处都留了回退。
 */
export function RunStatusBadge({ status, className }: Props) {
  return (
    <Badge variant={RUN_STATUS_VARIANT[status] ?? 'outline'} className={cn(className)}>
      {RUN_STATUS_LABEL[status] ?? status}
    </Badge>
  )
}
