import { errorDetail, errorMessage } from '@/lib/errors'

type Props = {
  error: unknown
  className?: string
}

/**
 * 统一的后端错误展示。
 *
 * 两行：第一行是 type 对应的中文说明，第二行小字放后端原始 detail。
 * 分两行是因为两者的读者不同——用户看第一行就够，排查时看第二行。
 *
 * 任何要显示后端错误的地方都用这个组件，不要各处自己 `String(error)`。
 */
export function ErrorText({ error, className }: Props) {
  if (!error) return null

  const detail = errorDetail(error)

  return (
    <div className={className}>
      <p className="text-destructive text-sm">{errorMessage(error)}</p>
      {detail && <p className="text-muted-foreground mt-1 text-xs">{detail}</p>}
    </div>
  )
}
