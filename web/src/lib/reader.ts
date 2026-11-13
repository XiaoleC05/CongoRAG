/**
 * 阅读器的纯函数工具。
 *
 * 【为什么从 ReaderPage.tsx 里搬出来】这几段逻辑原来都内联在组件里（parseBBox、
 * 目录高亮、bbox 定位、悬停翻译的断句），它们的共同点是**不依赖 PDF.js、
 * 也不依赖 React**——只吃参数、吐结果。留在组件里有两个代价：
 *   1. 测不了：渲染 ReaderPage 要 mock 整个 pdfjs-dist（还有 Worker），
 *      为了验证"bbox 非法值返回 null"去搭那套脚手架不划算，结果就是这块
 *      从来没测过，边界值全是靠手点；
 *   2. 复用不了：窄屏抽屉与桌面双栏都要用同一份断句/定位逻辑，内联写法
 *      只能复制一遍。
 * 搬到这里之后 reader.test.ts 是纯 Node 用例，毫秒级跑完全部边界。
 */

/** 归一化的引用框：[x0, y0, x1, y1]，0~1，左上原点（与 citation 事件一致）。 */
export type BBox = [number, number, number, number]

/** 断句用的句末标点（中英文混排 + 换行）。 */
const SENTENCE_BOUNDARY = '.!?。！？；;\n'

/**
 * 从 query 里解析归一化 bbox（"x0,y0,x1,y1"），非法值当没有。
 *
 * 【为什么非法就当没有，而不是报错】这是 URL 上的一个可选参数：
 * 用户手改、旧链接、分享工具截断都可能让它变形。阅读器本身仍然可用，
 * 为一个画不出来的高亮框把整页变成错误页是过度反应。
 *
 * 【为什么要求 x1 > x0 / y1 > y0】零宽/负宽的框画出来是一根线或反向的框，
 * 看起来像"高亮跑到别处去了"，比没有高亮更难排查。
 */
export function parseBBox(raw: string | null): BBox | null {
  if (!raw) return null
  const parts = raw.split(',').map(Number)
  if (parts.length !== 4 || parts.some((v) => !Number.isFinite(v))) return null
  const [x0, y0, x1, y1] = parts
  if (x1 <= x0 || y1 <= y0) return null
  return [x0, y0, x1, y1]
}

/** bbox → 覆盖层的内联样式（按百分比定位，缩放/翻页都不会失配）。 */
export type BBoxStyle = { left: string; top: string; width: string; height: string }

/**
 * 把归一化 bbox 换算成 CSS 百分比。
 *
 * 【为什么再校验一次】调用方可能拿到已经解析过的 BBox，也可能是别处
 * （比如将来的阅读进度记录）传进来的裸数组。这里对非有限值/零面积兜底返回
 * null——渲染层拿到 null 就不画覆盖层，比画一个 NaN% 的框好。
 */
export function bboxStyle(bbox: BBox | null | undefined): BBoxStyle | null {
  if (!bbox || bbox.length !== 4) return null
  const [x0, y0, x1, y1] = bbox
  if (![x0, y0, x1, y1].every((v) => Number.isFinite(v))) return null
  if (x1 <= x0 || y1 <= y0) return null
  return {
    left: `${x0 * 100}%`,
    top: `${y0 * 100}%`,
    width: `${(x1 - x0) * 100}%`,
    height: `${(y1 - y0) * 100}%`,
  }
}

/**
 * 当前页落在目录的哪一项（返回下标；不在任何一节里返回 -1）。
 *
 * 【为什么用二分而不是一次遍历】目录长度是章节数（几十到几百），而调用点
 * 原来在一个 map 里对每一项都重新 reduce 一遍——O(n²)，目录长的时候每次
 * 翻页都要算几千次。二分把复杂度压到 O(log n)，而且这个函数是纯的，
 * 可以用 useMemo 包一层，只在 outline/pageNo 变化时重算。
 *
 * 【前提：outline 按 page 非递减】（契约里目录"按原文顺序排列"）。乱序数据
 * 二分的结果没有意义——这不是要在这里兜的错，是上游数据坏了。
 */
export function activeOutlineIndex(
  outline: ReadonlyArray<{ page: number }>,
  page: number,
): number {
  let lo = 0
  let hi = outline.length - 1
  let found = -1
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    if (outline[mid].page <= page) {
      found = mid
      lo = mid + 1
    } else {
      hi = mid - 1
    }
  }
  return found
}

/**
 * 取 offset 所在的句子（悬停翻译用）。
 *
 * 【为什么按标点切而不是按固定长度】学术文本的句子普遍长；切在半个从句上
 * 翻译出来是错的，而按 .!?。！？；;
 切分至少保证语义单元完整。
 *
 * 【为什么把 offset 夹到 [0, length]】调用方给的是 DOM Range 的 startOffset；
 * 文本在两次调用之间被替换（翻页）时它可能越界，不夹的话 slice 会静默给出
 * 空串或从错位置开始。
 */
export function splitSentences(full: string, offset: number): string {
  // 夹到最后一个字符上而不是 full.length：offset 越界时用户想看的显然是
  // 末尾那句，夹到 length 会落到"最后一个字符之后"，切出来是空串。
  const at = Math.min(Math.max(offset, 0), Math.max(full.length - 1, 0))
  let start = 0
  let end = full.length
  for (let i = at - 1; i >= 0; i -= 1) {
    if (SENTENCE_BOUNDARY.includes(full[i])) {
      start = i + 1
      break
    }
  }
  for (let i = at; i < full.length; i += 1) {
    if (SENTENCE_BOUNDARY.includes(full[i])) {
      end = i + 1
      break
    }
  }
  return full.slice(start, end).trim()
}
