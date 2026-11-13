/**
 * openapi-fetch 结果的统一解包。
 *
 * 【为什么需要这一层】openapi-fetch 用**返回值**表示失败（`{ data, error, response }`），
 * 而 TanStack Query 和 React 事件处理都认"抛异常"。每个 hook 各写一遍
 * `if (error) throw error` 看着只有一行，坏处是兜底写法会漂——本次重构前
 * useDocuments.ts 里有 11 处手写 fetch，最典型的两处漂移是：
 *   1. `throw new Error(await res.text())`：把 RFC 7807 的 Problem 对象包进普通
 *      Error。`lib/errors.ts` 的 errorType()/errorMessage() 靠 `err.type` 选文案，
 *      包一层之后永远走兜底分支，界面上出现一行 JSON 原文（违反 web/README §4）。
 *   2. 204 没有响应体时把 `undefined` 当成功数据返回，调用方在后面某个
 *      `data!.xxx` 上炸掉——报错点离真正的原因隔了好几层。
 *
 * 【为什么原样抛 error 对象，不 new Error(...)】Problem 对象就是契约里的数据
 * 形状，`type` 是机器读的枚举。包一层等于把机器可读的信息降级成字符串：
 * ErrorText 的"中文一句话 + 原始 detail"两行展示、引导页按
 * embedding_change_requires_reindex 弹确认框，全都依赖它。**这里抛的必须是
 * 同一个对象**（测试里用 toBe 钉住，防的是下一个人"顺手"再包一层）。
 *
 * 【没有 error 但 response.ok 为假怎么办】契约里有几处错误响应没有 body
 * （比如重复处理文档的 409），openapi-fetch 会给 `error: undefined`。这时没有
 * type 可用，但状态码对排查仍然有用，所以抛一个带状态码的 Error——它走
 * errorMessage() 的兜底分支，至少不说谎。
 */
import type { Schemas } from '@congorag/api-client'

/** 契约里的 RFC 7807 错误体。 */
export type Problem = Schemas['Problem']

/**
 * openapi-fetch 的返回值形状。
 *
 * 【为什么用宽松的可选字段，而不是它的精确联合类型】那个精确类型是
 * "成功分支 | 失败分支"的联合；拿它当参数类型时，函数体里访问 `data` 会因为
 * 另一半没有这个字段而报错。这里只声明"两者都可能缺席"，把区分责任交给
 * `response.ok` 和 `error` 两个判据，参数处仍然能正常推断出 data 的类型。
 */
export type FetchResult<T> = {
  data?: T
  error?: unknown
  response: Response
}

/** 请求失败但没有可解析的错误体时，给一句能看的说明。 */
function statusError(response: Response): Error {
  const code = response.status
  const text = response.statusText ? ` ${response.statusText}` : ''
  return new Error(`请求失败（HTTP ${code}${text}）`)
}

/**
 * 解包一次请求：成功返回 data，失败把 error 原样抛出去。
 *
 * 【data 为 undefined 也算失败】2xx 却没有响应体，说明这次调用的契约和
 * 服务端实现不一致（或者调用了一个本该无返回值的端点却用了这个函数）。
 * 与其让调用方拿着 undefined 往下跑、在后面几层爆掉，不如在这里说清楚。
 * 无返回值的端点请用下面的 unwrapVoid。
 */
export function unwrap<T>(result: FetchResult<T>): T {
  const { data, error, response } = result

  if (error !== undefined && error !== null) {
    // 对象（RFC 7807 Problem）原样抛：见文件头"为什么原样抛"。
    if (typeof error === 'object') throw error
    // 响应体不是 JSON（网关 HTML 错误页、纯文本 502 之类），保留原文但包成 Error。
    throw new Error(`请求失败（HTTP ${response.status}）：${String(error)}`)
  }

  if (!response.ok) throw statusError(response)

  if (data === undefined) {
    throw new Error(`服务端返回了空响应体（HTTP ${response.status}），但这次调用需要它`)
  }

  return data
}

/**
 * 解包一次"没有响应体"的请求（204，或调用方不关心 202 的返回内容）。
 *
 * 【为什么单独一个函数】204 的成功响应本来就没有 data，用 unwrap 会被
 * "空响应体"误判成失败——一次成功的删除显示成失败。反过来把 unwrap 放宽成
 * "data 可以缺席"，需要 data 的调用方又得自己断言非空。两个出口各自表达
 * 意图，比一个可空返回值安全。
 */
export function unwrapVoid(result: FetchResult<unknown>): void {
  const { error, response } = result

  if (error !== undefined && error !== null) {
    if (typeof error === 'object') throw error
    throw new Error(`请求失败（HTTP ${response.status}）：${String(error)}`)
  }

  if (!response.ok) throw statusError(response)
}
