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

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 后端在 409 里给的说明（走 lib/errors.ts 的 errorMessage 转成中文） */
  message: string
  pending: boolean
  /** 用户已经确认——调用方带着 allowEmbeddingReset: true 重发那一次请求 */
  onConfirm: () => void
}

/**
 * 「换 embedding 模型」的确认框（issue #39 的流程，issue #83 里复用）。
 *
 * 【它是确认，不是报错】服务端发现库里的向量不属于这个模型时返回 409
 * `embedding_change_requires_reindex`。但那不是"你做错了什么"——用户改
 * 任何一个字段都过不去，唯一的出路是确认"清空并重建"。所以这里弹的是
 * AlertDialog（role="alertdialog"、默认聚焦取消），不是一行红字。
 *
 * 【为什么用 AlertDialog 而不是 Dialog】界面引用方案与 web/README.md §20：
 * 破坏性 / 不可逆的操作走 AlertDialog。清空全部向量确实是不可逆的——
 * 重建是异步的，期间检索返回空结果。
 *
 * 【为什么单独抽成一个组件，而不是在设置页里内联】这段流程和引导页
 * （OnboardingPage.tsx）里那份是同一件事：同样的判据、同样的两段正文、
 * 同样的"不阻止默认行为的话弹窗会立刻关闭、而请求还在飞"。
 *
 * **注意**：OnboardingPage.tsx 里现在仍有它自己的一份，本批次不允许改那个
 * 文件（它不在改动清单里），所以两处暂时并存。搬到这个组件上是一次纯粹的
 * 去重，等那个文件解禁时做——判据只有一条：谁改那一段，谁负责对齐这里的
 * 文案与行为。
 */
export function EmbeddingResetDialog({ open, onOpenChange, message, pending, onConfirm }: Props) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>要换 embedding 模型吗？</AlertDialogTitle>
          <AlertDialogDescription>{message}</AlertDialogDescription>
        </AlertDialogHeader>

        <p className="text-muted-foreground text-sm">
          确认之后服务端会在同一个事务里清空这些向量、把列改成新模型的维度，
          并把全部文档重新排队重建。重建是后台异步做的，期间检索会返回空结果；
          文档列表里能看到它们重新变成「处理中」，跑完就恢复。
        </p>

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>取消</AlertDialogCancel>
          <AlertDialogAction
            disabled={pending}
            onClick={(e) => {
              // 不阻止的话弹窗会立刻关闭，而请求还在飞——用户看不到
              // "正在探测"这个中间态。
              e.preventDefault()
              onConfirm()
            }}
          >
            {pending ? '探测中…' : '清空并重建'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
