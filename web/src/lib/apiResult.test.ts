import { describe, expect, it } from 'vitest'

import { unwrap, unwrapVoid } from '@/lib/apiResult'

/**
 * 这一层要守的是一条**错误传递链**，不是一个函数：
 *
 *   openapi-fetch 的 error 对象 → unwrap 原样抛出 → lib/errors.ts 的
 *   errorType()/errorMessage() 认出 type → 界面显示中文而不是 JSON
 *
 * 所以断言的重点不是"抛了异常"，而是"抛出来的是不是**同一个对象**"。
 * 用 toBe 而不是 toEqual：后者在有人 `throw { ...error }` 复制一份时
 * 仍然会绿，但复制过的对象已经丢了原型链/引用一致性，而这条链真正要的
 * 是"别包、别复制、别转字符串"。
 */

/** 一次成功的响应：2xx + JSON body。 */
const success = <T,>(data: T, status = 200) => ({
  data,
  error: undefined,
  response: new Response(JSON.stringify(data), { status }),
})

/** 一次失败响应：错误体是可以解析的 RFC 7807 Problem。 */
const problemFailure = (problem: { type: string; title: string; status: number }) => ({
  data: undefined,
  error: problem,
  response: new Response(JSON.stringify(problem), { status: problem.status }),
})

describe('unwrap', () => {
  it('成功时把 data 原样返回（不做拷贝/包装）', () => {
    const payload = { items: [{ id: 'doc-1' }], nextCursor: null }
    expect(unwrap(success(payload))).toBe(payload)
  })

  it('Problem 对象原样抛出：同一个引用，type 仍然可读', () => {
    const problem = { type: 'not_found', title: '没找到', status: 404 }
    let caught: unknown
    try {
      unwrap(problemFailure(problem))
    } catch (err) {
      caught = err
    }
    // 同一引用：包成 new Error(JSON.stringify(...)) 会让这条红。
    expect(caught).toBe(problem)
    // 这条钉的是"包一层之后 type 就没了"的那个后果。
    expect((caught as { type?: string }).type).toBe('not_found')
  })

  it('错误体不是 JSON 时包成 Error，但保留响应原文便于排查', () => {
    const result = {
      data: undefined,
      error: '<html>502 Bad Gateway</html>',
      response: new Response('<html>502 Bad Gateway</html>', { status: 502 }),
    }
    expect(() => unwrap(result)).toThrow(/502/)
    expect(() => unwrap(result)).toThrow(/Bad Gateway/)
  })

  it('2xx 但响应体为空：抛可读错误，而不是把 undefined 交给调用方', () => {
    const result = { data: undefined, error: undefined, response: new Response(null, { status: 200 }) }
    expect(() => unwrap(result)).toThrow(/空响应体/)
  })

  it('非 2xx 但没有可解析错误体（如无 body 的 409）：抛带状态码的错误', () => {
    const result = { data: undefined, error: undefined, response: new Response(null, { status: 409 }) }
    expect(() => unwrap(result)).toThrow(/409/)
  })
})

describe('unwrapVoid', () => {
  it('204 的成功响应直接通过（没有 data 不是失败）', () => {
    expect(() =>
      unwrapVoid({ data: undefined, error: undefined, response: new Response(null, { status: 204 }) }),
    ).not.toThrow()
  })

  it('失败仍然原样抛出 Problem 对象', () => {
    const problem = { type: 'conflict', title: '冲突', status: 409 }
    // 这里只能自己 catch：toThrow 匹配的是 message，而 Problem 没有 message 字段，
    // 用它断言等于没测到"抛的是不是同一个对象"。
    try {
      unwrapVoid(problemFailure(problem))
    } catch (err) {
      expect(err).toBe(problem)
    }
  })

  it('非 2xx 且没有错误体时抛带状态码的错误', () => {
    expect(() =>
      unwrapVoid({ error: undefined, response: new Response(null, { status: 500 }) }),
    ).toThrow(/500/)
  })
})
