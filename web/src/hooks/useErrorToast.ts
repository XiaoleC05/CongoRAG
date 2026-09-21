import { createElement } from 'react'
import { toast } from 'sonner'

import { ErrorToast } from '@/components/ErrorToast'
import { errorPresentation } from '@/lib/errors'

/**
 * 写操作失败的统一出口。
 *
 * 返回 true 表示【调用方还得自己把这个错误渲染出来】；返回 false 表示
 * toast 已经说过了，调用方不要再渲染一遍——同一条错误同时出现在两个
 * role="alert" 上，读屏软件会念两遍（见 web/README.md 的错误展示约定）。
 *
 * 【判据不在这里】走 lib/errors.ts 的 errorPresentation(err, 'mutation')。
 * 判据集中在一处，页面才不会各自漂移（例如把该贴在字段旁边的
 * invalid_argument 弹成一个一闪而过的提示）。
 *
 * 放在 hooks/ 而不是 lib/：它会产生副作用（往 toast 队列里塞一条），
 * 不是纯函数。见 web/README.md 的"放哪的判据"。
 */
export function useErrorToast() {
  return (err: unknown): boolean => {
    if (errorPresentation(err, 'mutation') !== 'toast') return true

    // 【为什么用 createElement 而不是 JSX】这个文件是 .ts 不是 .tsx，
    // 里面写不了 JSX 语法。它只是个转发，包一层组件是为了复用
    // ErrorToast 的 role="alert" 和信息层级。
    toast.error(createElement(ErrorToast, { error: err }))
    return false
  }
}
