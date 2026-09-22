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

type Document_ = Schemas['Document']

type Props = {
  /** null 表示没有待删除的目标，弹窗关闭 */
  target: Document_ | null
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: unknown
  onConfirm: () => void
}

/**
 * 删除一份文档的确认框（issue #91）。
 *
 * 【和 DeleteKnowledgeDialog 同一套做法，不是"长得像"】破坏性 / 不可逆的
 * 操作用 AlertDialog：它的 role 是 `alertdialog`、默认焦点落在取消上，
 * 语义上就是"请先确认"；用 Dialog 的话，两者在读屏软件里念出来是一样的，
 * 用户分不出哪个是危险操作（web/README.md §20）。
 *
 * 【正文必须点明连带代价，不能只写"确定删除吗"】数据库那边文档与分块的
 * 外键是 ON DELETE CASCADE，向量也跟着走；而这份文档此前已经在参与检索——
 * 删掉之后**同一个问题的回答会变**。用户点之前得知道这件事，事后才发现
 * 检索结果变了是查不出来的那类问题。
 *
 * 【确认按钮文案是"确定"而不是"删除"】和 DeleteKnowledgeDialog / Dify 一致：
 * 红色已经表达了危险性，按钮再重复一遍动词反而啰嗦。
 */
export function DeleteDocumentDialog({ target, onOpenChange, pending, error, onConfirm }: Props) {
  return (
    <AlertDialog open={!!target} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除「{target?.filename}」？</AlertDialogTitle>
          <AlertDialogDescription>
            此操作不可撤销，这份文档的分块与向量会一并删除，之后的检索结果里不会再出现它的内容。
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
