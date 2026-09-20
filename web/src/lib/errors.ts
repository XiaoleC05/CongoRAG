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
