import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

type ModelSummary = Schemas['ModelSummary']

type Props = {
  /** null 表示没有待删除的目标，弹窗关闭 */
  target: ModelSummary | null
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: unknown
  onConfirm: () => void
}

/**
 * 删除一个模型条目的确认框。
 *
 * 【正文必须写明"用量记录一起没"】契约里 `deleteModel` 的 description 用
 * 警告符号点明了这件事：`token_usage.model_id` 的外键是 `ON DELETE CASCADE`
 * （migrations/0003），所以删掉这个模型会把它历史上的 token 用量一起删掉——
 * 用量页上那些数字会跟着变小。这是既有表结构决定的，不是这次新引入的行为，
 * 但对用户来说后果一样：**他可能正在用那些数字对账**。不写清就等于让他在
 * 事后发现账单对不上。
 *
 * 【"当前生效的那个删不掉"只说明、不预判】服务端对"该 kind 里当前生效的
 * 那一条"返回 409。前端不去自己判断"这一条是不是当前生效的"——判据是
 * `LatestByKind`（同 kind 里 createdAt 最新），复刻到前端就意味着又多一处
 * 必须和后端同步的规则，而且它一旦漂移，按钮的状态就是错的。这里只把
 * 这条规则写进正文，让服务端当那个强制点（§6 的同一条原则），409 由
 * lib/errors.ts 归到 conflict，按 toast 呈现。
 */
export function DeleteModelDialog({ target, onOpenChange, pending, error, onConfirm }: Props) {
  return (
    <AlertDialog open={!!target} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除模型「{target?.modelId}」？</AlertDialogTitle>
          <AlertDialogDescription>
            此操作不可撤销。这条模型的 token 用量记录会一并删除——
            用量页上那些数字会跟着变小。
          </AlertDialogDescription>
        </AlertDialogHeader>

        <p className="text-muted-foreground text-sm">
          当前生效的那一条删不掉（服务端会拒绝）：删了它，发消息和检索会立刻失败，
          而且失败点离你这个操作很远。要换模型，先把新的那条建出来。
        </p>

        <ErrorText error={error} />

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>取消</AlertDialogCancel>
          <AlertDialogAction
            disabled={pending}
            onClick={(e) => {
              // 不阻止的话 AlertDialog 会立刻关闭，请求还在飞
              e.preventDefault()
              onConfirm()
            }}
          >
            {pending ? '删除中…' : '确定'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
