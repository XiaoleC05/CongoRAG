import { describe, expect, it } from 'vitest'

import { activeOutlineIndex, bboxStyle, parseBBox, splitSentences } from '@/lib/reader'

/**
 * 这一组用例守的是阅读器里"不会报错、只会画错/翻错"的那类逻辑：
 * bbox 画歪了、目录高亮停在上上节、悬停翻译取到半句话。
 * 它们全部是纯函数，边界值可以在毫秒级跑完。
 */

describe('parseBBox', () => {
  it('合法值解析成四元组', () => {
    expect(parseBBox('0.1,0.2,0.3,0.4')).toEqual([0.1, 0.2, 0.3, 0.4])
  })

  it('空值/缺失参数当没有', () => {
    expect(parseBBox(null)).toBeNull()
    expect(parseBBox('')).toBeNull()
  })

  it('段数不对当没有', () => {
    expect(parseBBox('0.1,0.2,0.3')).toBeNull()
    expect(parseBBox('0.1,0.2,0.3,0.4,0.5')).toBeNull()
  })

  it('非数字当没有（URL 被手改/截断时会出现）', () => {
    expect(parseBBox('a,0.2,0.3,0.4')).toBeNull()
    expect(parseBBox('0.1,Infinity,0.3,0.4')).toBeNull()
  })

  it('零面积/反向的框当没有（画出来是一根线或反向框）', () => {
    expect(parseBBox('0.3,0.2,0.3,0.4')).toBeNull()
    expect(parseBBox('0.1,0.4,0.3,0.2')).toBeNull()
  })
})

describe('bboxStyle', () => {
  it('合法 bbox 换算成百分比', () => {
    const style = bboxStyle([0.1, 0.2, 0.3, 0.5])
    expect(style).not.toBeNull()
    // 【不要对宽高做字符串全等】(0.3-0.1)*100 这类浮点运算的结果可能是
    // "19.999999999999996%" 也可能是 "20%"，取决于具体数值。断言数值即可。
    expect(style!.left).toBe('10%')
    expect(style!.top).toBe('20%')
    expect(Number.parseFloat(style!.width)).toBeCloseTo(20)
    expect(Number.parseFloat(style!.height)).toBeCloseTo(30)
  })

  it('null / 非法值返回 null（调用方据此不画覆盖层）', () => {
    expect(bboxStyle(null)).toBeNull()
    expect(bboxStyle(undefined)).toBeNull()
    expect(bboxStyle([Number.NaN, 0, 0.3, 0.4])).toBeNull()
    expect(bboxStyle([0.2, 0.2, 0.2, 0.4])).toBeNull()
  })
})

describe('activeOutlineIndex', () => {
  const outline = [{ page: 1 }, { page: 3 }, { page: 7 }, { page: 12 }]

  it('第一页在第一节里', () => {
    expect(activeOutlineIndex(outline, 1)).toBe(0)
  })

  it('页码正好等于某节时落在那一节（而不是上一节）', () => {
    expect(activeOutlineIndex(outline, 7)).toBe(2)
  })

  it('页码落在两节之间时取前一节', () => {
    expect(activeOutlineIndex(outline, 10)).toBe(2)
  })

  it('页码超出最后一节时取最后一节', () => {
    expect(activeOutlineIndex(outline, 999)).toBe(3)
  })

  it('第一页之前没有任何高亮', () => {
    expect(activeOutlineIndex(outline, 0)).toBe(-1)
    expect(activeOutlineIndex([{ page: 5 }], 1)).toBe(-1)
  })

  it('空目录返回 -1', () => {
    expect(activeOutlineIndex([], 3)).toBe(-1)
  })

  it('同一页有多个标题时取最后一个（同级标题的常见形态）', () => {
    expect(activeOutlineIndex([{ page: 2 }, { page: 2 }, { page: 2 }], 2)).toBe(2)
  })
})

describe('splitSentences', () => {
  it('取 offset 所在的句子（英文句号切分）', () => {
    const full = 'First sentence. Second sentence! Third one?'
    expect(splitSentences(full, 17)).toBe('Second sentence!')
  })

  it('中文标点也能切', () => {
    const full = '第一句。第二句！第三句？'
    expect(splitSentences(full, 4)).toBe('第二句！')
  })

  it('换行也当句子边界（标题行/列表项）', () => {
    const full = '标题\n正文内容'
    expect(splitSentences(full, 4)).toBe('正文内容')
  })

  it('没有标点时返回整段（不能返回空串）', () => {
    expect(splitSentences('没有标点的一段文字', 3)).toBe('没有标点的一段文字')
  })

  it('offset 为 0 / 越界都夹回合法区间', () => {
    expect(splitSentences('Hello. World.', 0)).toBe('Hello.')
    expect(splitSentences('Hello. World.', 999)).toBe('World.')
    expect(splitSentences('Hello. World.', -1)).toBe('Hello.')
  })

  it('结果已 trim（传给翻译接口的文本不该带首尾空白）', () => {
    expect(splitSentences('A.  B.  ', 4)).toBe('B.')
  })
})
