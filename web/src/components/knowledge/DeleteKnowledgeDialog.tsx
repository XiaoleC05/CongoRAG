import type { Schemas } from '@congorag/api-client'

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
import { ErrorText } from '@/components/ErrorText'

type KnowledgeBase = Schemas['KnowledgeBase']

type Props = {
  /** null 表示没有待删除的目标，弹窗关闭 */
  target: KnowledgeBase | null
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: unknown
  onConfirm: () => void
}

/**
 * 删除确认。
 *
 * 用 AlertDialog 而不是 Dialog：这是破坏性操作，
 * AlertDialog 的语义（role="alertdialog"、默认聚焦取消）更适合。
 *
 * 正文里点明"连带删除文档和分块"——数据库那边是 ON DELETE CASCADE，
 * 不可逆，用户必须知道。
 *
 * 确认按钮的文案是"确定"而不是"删除"，和 Dify 一致：
 * 红色已经表达了危险性，按钮再重复一遍动词反而啰嗦。
 */
export function DeleteKnowledgeDialog({
  target,
  onOpenChange,
  pending,
  error,
  onConfirm,
}: Props) {
  return (
    <AlertDialog open={!!target} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除「{target?.name}」？</AlertDialogTitle>
          <AlertDialogDescription>
            此操作不可撤销，知识库下的文档和分块会一并删除。
          </AlertDialogDescription>
        </AlertDialogHeader>

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
