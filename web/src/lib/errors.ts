/**
 * 后端错误的处理。所有错误都是 RFC 7807：
 *
 *   { type, title, status, detail }
 *
 * `type` 是机器读的枚举，前端按它选文案——**不要按 `detail` 的文案分支**，
 * 那是后端可以随时改的。`detail` 只作为补充信息展示。
 */

/**
 * type 取值 → 给用户看的一句话。
 *
 * 【这张表要和 apps/api/internal/api/problem.go 的 classify() 对齐】
 * 那边加一个 sentinel 就多一个 type，这里要跟着补一条；
 * 漏了不会报错，只是会落到下面的兜底分支显示后端的原文。
 */
const MESSAGES: Record<string, string> = {
  invalid_argument: '提交的内容不合法',
  not_found: '要找的东西不存在',
  conflict_duplicate_key: '已经有一个同名的了',
  // 目前只有"换 embedding 模型会和库里已有的向量冲突"这一种，detail 里有
  // 具体是哪张表、多少行，所以这里只给一句概括，细节靠 detail 补充。
  conflict: '和已有的数据冲突了，这次改动没有生效',
  context_overflow: '上下文超出了模型的窗口，减少一些输入或换窗口更大的模型',
  upstream_llm_error: '上游模型服务出错，检查一下 API Key 和配额',
  internal_error: '服务内部错误',
}

/**
 * 错误该以什么形式呈现给用户。
 *
 *   'page'   —— 这次失败之后，当前这个视图没有东西可渲染了（整页占位）
 *   'inline' —— 用户必须改这个字段才能重试（错误要显示在输入框旁边）
 *   'toast'  —— 界面还是完整的，重试一次就行（一闪而过的提示足够）
 */
export type ErrorPresentation = 'page' | 'inline' | 'toast'

/** 错误出在【读】请求上还是【写】请求上——同一个 type 在这两者下的归宿不同。 */
export type ErrorContext = 'query' | 'mutation'

/**
 * 一个错误该用哪种形式呈现。
 *
 * 【为什么按 type 先分、再让 ctx 收口】type 决定"用户能不能自己解决"，
 * ctx 决定"页面还剩不剩内容"——后者只有调用方知道，所以必须传进来。
 *
 *   invalid_argument / conflict_duplicate_key / context_overflow
 *       用户得改输入才能过，错误得贴在字段旁边。但读请求没有"字段"可贴
 *       （查询参数不是用户当场填的），只能整页显示。
 *   not_found
 *       目标不存在。读请求遇上它，这个页面本来就没内容可显示；
 *       写请求遇上它（比如删一条已经被别人删掉的行）页面还是好的，提示一下即可。
 *   conflict / upstream_llm_error / internal_error / 认不出的 type
 *       重试一次就行。但读请求失败后页面是空白的，得留在页面上顶着。
 */
export function errorPresentation(err: unknown, ctx: ErrorContext): ErrorPresentation {
  const p = asProblem(err)
  switch (p?.type) {
    case 'invalid_argument':
    case 'conflict_duplicate_key':
    case 'context_overflow':
      return ctx === 'query' ? 'page' : 'inline'
    case 'not_found':
      return ctx === 'mutation' ? 'toast' : 'page'
    default:
      return ctx === 'query' ? 'page' : 'toast'
  }
}

type ProblemLike = { type?: string; title?: string; detail?: string }

function asProblem(err: unknown): ProblemLike | null {
  return err && typeof err === 'object' ? (err as ProblemLike) : null
}

/**
 * 一句话说明发生了什么。优先用 type 对应的文案，认不出则回退到后端的原文。
 */
export function errorMessage(err: unknown): string {
  const p = asProblem(err)
  if (p) {
    if (p.type && MESSAGES[p.type]) return MESSAGES[p.type]
    // 认不出的 type（后端加了新的但前端还没跟上）也尽量给出能看的东西
    const fallback = p.detail ?? p.title ?? p.type
    if (fallback) return fallback
  }
  return String(err)
}

/**
 * 后端给的原始说明，用于排查。没有则返回 undefined。
 *
 * 有 type 映射时它是补充信息（可能是英文的、偏技术的），
 * 所以单独拿出来用小字显示，而不是塞进主文案。
 */
export function errorDetail(err: unknown): string | undefined {
  const p = asProblem(err)
  if (!p) return undefined
  // 主文案已经用了 detail 的情况不重复显示
  if (p.type && MESSAGES[p.type] && p.detail) return p.detail
  if (p.type && MESSAGES[p.type]) return undefined
  return undefined
}
