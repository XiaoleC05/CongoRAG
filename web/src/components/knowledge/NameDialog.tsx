import { useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { ErrorText } from '@/components/ErrorText'
import { validateName } from '@/lib/validation'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string
  submitLabel: string
  /** 新建时不传，改名为原名 */
  initialValue?: string
  pending: boolean
  error: unknown
  onSubmit: (name: string) => void
}

/**
 * 只有一个"名字"输入框的表单弹窗。新建和改名共用。
 *
 * 抽出来是因为两者的差别只有标题、按钮文案和初始值——
 * 写两份的话，将来加校验或改交互要改两处，而且容易漏一处。
 *
 * 【待补】客户端的空值校验没做：留空提交时由后端返回 400，
 * 前端把错误显示出来。对本地工具够用，但网络往返是白费的。
 */
export function NameDialog({
  open,
  onOpenChange,
  title,
  description,
  submitLabel,
  initialValue = '',
  pending,
  error,
  onSubmit,
}: Props) {
  // 输入框的初始值由【调用方换 key 触发重新挂载】来重置，不在这里用 effect 监听 open。
  // effect 里 setState 会多渲染一次，oxlint 的 react(set-state-in-effect) 也会报。
  // 见 KnowledgeBasesPage 里给这个组件传的 key。
  const [name, setName] = useState(initialValue)

  // 客户端校验只省一次白跑的往返；真正的强制在后端（见 lib/validation.ts）。
  const clientError = validateName(name)
  // 刚打开时输入框是空的，这时不提示——一进来就一片红字很烦。
  const showHint = name !== '' && !!clientError

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          onSubmit={(e) => {
            e.preventDefault() // 不拦的话浏览器会整页刷新
            onSubmit(name)
          }}
        >
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription>{description}</DialogDescription>}
          </DialogHeader>

          <div className="py-4">
            <Input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="知识库名字"
            />
            {showHint && (
              <p className="text-muted-foreground mt-2 text-sm">{clientError}</p>
            )}
            <ErrorText error={error} className="mt-2" />
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={pending}
            >
              取消
            </Button>
            <Button type="submit" disabled={pending || !!clientError}>
              {pending ? '处理中…' : submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
