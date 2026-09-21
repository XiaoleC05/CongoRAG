import { describe, expect, it } from 'vitest'

import { errorDetail, errorMessage, errorPresentation } from '@/lib/errors'
import type { ErrorContext, ErrorPresentation } from '@/lib/errors'

/**
 * 呈现方式的判据表。写成数据而不是一堆 it()，是因为真正要守的约束是
 * "7 个 type × 2 个 ctx 每一格都有明确归宿"——表格漏一格时肉眼看不出来，
 * 遍历一遍就能。
 *
 * type 的取值范围和 apps/api/internal/api/problem.go 的 classify() 对齐。
 */
const TYPES = [
  'invalid_argument',
  'not_found',
  'conflict_duplicate_key',
  'conflict',
  'context_overflow',
  'upstream_llm_error',
  'internal_error',
] as const

/** [type, query 时的归宿, mutation 时的归宿] */
const TABLE: Array<[string, ErrorPresentation, ErrorPresentation]> = [
  // 要用户改输入 → 写操作贴字段旁边；读操作没有字段可贴，只能整页
  ['invalid_argument', 'page', 'inline'],
  ['conflict_duplicate_key', 'page', 'inline'],
  ['context_overflow', 'page', 'inline'],
  // 目标不存在 → 读操作页面空了；写操作页面还在，提示一下
  ['not_found', 'page', 'toast'],
  // 重试就行 → 写操作弹 toast；读操作页面空了，得留在页面上
  ['conflict', 'page', 'toast'],
  ['upstream_llm_error', 'page', 'toast'],
  ['internal_error', 'page', 'toast'],
]

describe('errorPresentation', () => {
  it('表格覆盖了契约里全部 7 个 type，一个不漏一个不多', () => {
    // 比集合而不是比顺序：下面那张表是按"归宿"分组排的，
    // 和 TYPES 的字母序不同——要守的是覆盖，不是排版。
    expect([...TABLE.map(([t]) => t)].sort()).toEqual([...TYPES].sort())
  })

  it.each(TABLE)('%s：query → %s，mutation → %s', (type, whenQuery, whenMutation) => {
    expect(errorPresentation({ type }, 'query')).toBe(whenQuery)
    expect(errorPresentation({ type }, 'mutation')).toBe(whenMutation)
  })

  // 后端加了新 type 而前端还没跟上的情况：按"能重试"处理，
  // 但读失败的页面同样是空白的，不能弹个 toast 就算完。
  describe('认不出的 type 落兜底分支', () => {
    it('mutation 时按可重试处理', () => {
      expect(errorPresentation({ type: 'brand_new_type' }, 'mutation')).toBe('toast')
    })

    it('query 时仍然留在页面上', () => {
      expect(errorPresentation({ type: 'brand_new_type' }, 'query')).toBe('page')
    })
  })

  // 非 RFC 7807 的失败：网络断了（fetch 抛 TypeError）、代码里手抛的字符串等。
  // 这些同样没有 type，走兜底分支。
  describe('不是 Problem 形状的失败', () => {
    const notAProblem: unknown[] = [
      { type: undefined },
      {},
      new TypeError('Failed to fetch'),
      'not_found', // 字符串里恰好有 type 的取值，但它不是 Problem
      null,
      undefined,
    ]

    it.each(notAProblem.map((v) => [String(v), v] as const))(
      '%s：mutation → toast，query → page',
      (_label, value) => {
        expect(errorPresentation(value, 'mutation')).toBe('toast')
        expect(errorPresentation(value, 'query')).toBe('page')
      },
    )
  })

  it('两个 ctx 的取值都被覆盖到（防止将来只测一边）', () => {
    const contexts: ErrorContext[] = ['query', 'mutation']
    const outputs = contexts.map((c) => errorPresentation({ type: 'not_found' }, c))
    // not_found 在两个 ctx 下归宿不同——用它是为了证明 ctx 真的参与了判断，
    // 而不是被忽略掉
    expect(new Set(outputs).size).toBe(2)
  })
})

describe('errorMessage', () => {
  it('有映射时用 type 对应的中文，不用后端原文', () => {
    expect(
      errorMessage({ type: 'internal_error', title: '服务内部错误', detail: 'boom' }),
    ).toBe('服务内部错误')
  })

  it('每个已知 type 都有一句话，没有空文案', () => {
    for (const type of TYPES) {
      expect(errorMessage({ type })).not.toBe('')
    }
  })

  it('认不出的 type 回退到后端原文', () => {
    expect(errorMessage({ type: 'brand_new_type', detail: 'something broke' })).toBe(
      'something broke',
    )
  })

  it('detail 缺失时退到 title，再退到 type', () => {
    expect(errorMessage({ type: 'brand_new_type', title: '标题' })).toBe('标题')
    expect(errorMessage({ type: 'brand_new_type' })).toBe('brand_new_type')
  })

  it('不是对象时退到 String()，不抛异常', () => {
    expect(errorMessage('boom')).toBe('boom')
    expect(errorMessage(undefined)).toBe('undefined')
  })
})

describe('errorDetail', () => {
  it('有 type 映射时，detail 作为补充信息单独给出', () => {
    expect(errorDetail({ type: 'internal_error', detail: 'boom' })).toBe('boom')
  })

  it('有映射但没有 detail 时返回 undefined（不重复显示主文案）', () => {
    expect(errorDetail({ type: 'internal_error' })).toBeUndefined()
  })

  // 主文案已经用了 detail（认不出 type 的分支），这里不能再重复一遍
  it('认不出的 type 且主文案用了 detail 时返回 undefined', () => {
    expect(errorDetail({ type: 'brand_new_type', detail: 'something broke' })).toBeUndefined()
  })

  it('不是 Problem 时返回 undefined', () => {
    expect(errorDetail('boom')).toBeUndefined()
    expect(errorDetail(undefined)).toBeUndefined()
  })
})
