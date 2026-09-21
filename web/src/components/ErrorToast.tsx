import { errorDetail, errorMessage } from '@/lib/errors'

type Props = {
  error: unknown
}

/**
 * Toast 里的错误内容。跨域通用（不专属某个业务），所以放在 components/ 根下，
 * 和 ErrorText.tsx 并排。
 *
 * 信息层级和 ErrorText 一样是两行：第一行是 type 对应的中文说明，
 * 第二行小字放后端原始 detail。两者共用 lib/errors 的同一对函数——不这么做的话，
 * 同一条错误在页面上和在 toast 里会措辞不一致，用户会以为是两件事。
 *
 * 【为什么自带 role="alert"】sonner 自己渲染的 <li data-sonner-toast> 上
 * 没有 role，它只有一个 aria-live="polite" 的容器 section（那是给"通知"用的
 * 礼貌级别，要等读屏软件把当前这句念完才轮到它）。错误必须立刻被读出来，
 * 所以在【我们自己的】根节点上显式声明 role="alert"（隐含 assertive），
 * 不依赖 sonner 的 DOM 结构——它换个大版本就可能变。
 *
 * 【不要在这里再加 aria-live】role="alert" 本身已经是一个 assertive 的
 * live region，两个属性同时写会被部分读屏软件当成两个区域，同一条内容念两遍。
 */
export function ErrorToast({ error }: Props) {
  const detail = errorDetail(error)

  return (
    <div role="alert">
      <p className="text-destructive text-sm">{errorMessage(error)}</p>
      {detail && <p className="text-muted-foreground mt-1 text-xs">{detail}</p>}
    </div>
  )
}
