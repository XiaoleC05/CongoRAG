import { ChevronLeft, ChevronRight, ExternalLink, Languages, List, X, ZoomIn, ZoomOut } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import * as pdfjs from 'pdfjs-dist'
import type { PDFDocumentProxy } from 'pdfjs-dist'
// 【为什么用 ?worker 而不是 ?url】?url 会产出独立的 .mjs 资源，交给后端
// 静态服务时 MIME 由 Go 的 mime 表猜；猜错（text/plain）会被浏览器的
// module script 校验直接拒绝。?worker 让 Vite 走自己的 worker 打包管线，
// 产物是标准 .js + new Worker(...)，不依赖服务端 MIME 判断。
import PdfWorker from 'pdfjs-dist/build/pdf.worker.min.mjs?worker'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { ErrorText } from '@/components/ErrorText'
import { PageFallback } from '@/components/PageFallback'
import { useDocumentOutline, useDocumentReferences } from '@/hooks/useDocuments'
import { unwrap } from '@/lib/apiResult'
import { activeOutlineIndex, bboxStyle, parseBBox, splitSentences } from '@/lib/reader'
import type { BBox } from '@/lib/reader'

/** 文档元信息（本页只用到这几项，不引 api-client 的完整类型）。 */
type DocumentMeta = {
  id: string
  knowledgeBaseId: string
  filename: string
  pageCount: number
  chunkCount: number
  status: string
}

/** 目录的一行（契约 OutlineEntry 的消费侧形状，理由见下面 ReaderView 里的注释）。 */
type OutlineRow = { title: string; page: number; level: number }

/** 参考文献列表（契约 ReferenceEntry[]）。 */
type ReferenceList = Schemas['ReferenceEntry'][]

/**
 * PDF.js 的渲染任务被取消时抛这个异常。
 *
 * 【为什么按 name 判断而不是 instanceof】pdfjs-dist 的类型里没有把
 * RenderingCancelledException 作为公开类导出，instanceof 依赖内部实现；
 * 它的 name 是跨版本稳定的公开约定（PDF.js 各处 catch 也这么判）。
 */
function isRenderCancelled(error: unknown): boolean {
  return (error as { name?: string } | null)?.name === 'RenderingCancelledException'
}

/**
 * 论文阅读器（科研助手）——点 citation 的「在原文中查看」跳到这里。
 *
 * 【为什么自己用 pdfjs-dist 而不是 react-pdf】只需要"渲染 + 页码跳转 + 一个
 * 高亮框"，react-pdf 多一层组件抽象反而不好控制 canvas 与 overlay 的像素
 * 对齐。直连 pdfjs 的 API 少一个依赖、也少一个版本约束。
 *
 * 【bbox 的来源】citation 事件里带 page/bbox（归一化 0~1、左上原点），
 * 所以阅读器不需要再向后端要 blocks：URL 上的 bbox 直接按百分比盖在
 * canvas 上，缩放/翻页都不影响对齐。
 *
 * 【外层/内层为什么拆开】内层 ReaderView 以文档 id 为 key 挂载：换文档时
 * React 直接卸载重建，"把上一次的 pdf/错误状态清空"不需要在 effect 里
 * 同步 setState（那会触发 react-hooks 的 set-state-in-effect 告警，也会多
 * 一次无谓的级联渲染）。
 */
export default function ReaderPage() {
  const { id = '' } = useParams()
  const [search] = useSearchParams()
  const initialPage = Math.max(1, Number(search.get('page') ?? '1') || 1)
  const section = search.get('section')
  const bbox = parseBBox(search.get('bbox'))

  // 【citationPage 为什么单独传】bbox 属于"深链进来的那一页"，不是"当前页"。
  // 用户从第 7 页的引用跳进来、再翻到第 8 页时，高亮必须只留在第 7 页——
  // 一直画在画布上会盖在完全无关的内容上，看起来像引用定位错了。
  return (
    <ReaderView
      key={id}
      id={id}
      initialPage={initialPage}
      section={section}
      bbox={bbox}
      citationPage={bbox ? initialPage : null}
    />
  )
}

function ReaderView({
  id,
  initialPage,
  section,
  bbox,
  citationPage,
}: {
  id: string
  initialPage: number
  section: string | null
  bbox: BBox | null
  citationPage: number | null
}) {
  const [meta, setMeta] = useState<DocumentMeta | null>(null)
  const [metaError, setMetaError] = useState<unknown>(null)
  const [pdf, setPdf] = useState<PDFDocumentProxy | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [pageNo, setPageNo] = useState(initialPage)
  const [scale, setScale] = useState(1.4)
  const [rendering, setRendering] = useState(false)
  // 正在进行的那次 canvas 渲染。快速翻页/连续缩放时必须在开新渲染前取消它，
  // 见下面渲染 effect 里的注释。
  const renderTaskRef = useRef<{ cancel: () => void } | null>(null)
  // 翻译面板（科研助手 M3）：把当前页文本抽出来交给后端，用当前 chat 模型翻译。
  const [translating, setTranslating] = useState(false)
  // 按页保存翻译与原文（state 而不是 ref：翻页/切双语时要触发渲染）。
  const [translations, setTranslations] = useState<Record<number, string>>({})
  const [pageTexts, setPageTexts] = useState<Record<number, string>>({})
  const [translateError, setTranslateError] = useState<unknown>(null)
  // 学术翻译目标语言（M6 要求中文摘要/全文→学术英文）：双语/整页翻译共用。
  const [targetLang, setTargetLang] = useState<'zh-CN' | 'en'>('zh-CN')
  // 悬停翻译（官方 M3 的“局部划词/悬停翻译”）：鼠标停在原文句子上时，
  // 450ms 后翻译该句并浮层展示；划词仍然优先（有选中就不触发悬停）。
  const [hover, setHover] = useState<{
    x: number
    y: number
    text: string
    translation: string | null
  } | null>(null)
  const hoverTimer = useRef<number | null>(null)
  const hoverCache = useRef(new Map<string, string>())
  // 双语对照（科研助手 M3）：左 PDF / 右"原文 + 译文"双栏；翻页时两栏一起切到
  // 该页（页级联动）。划词翻译在原文栏选中文字触发。
  const [bilingual, setBilingual] = useState(false)
  const [selection, setSelection] = useState<{
    text: string
    translation: string | null
    loading: boolean
  } | null>(null)
  // 原文抽取缓存（去重网络/解析）；翻译/原文的展示值由上面的 state 派生。
  const pageTextCache = useRef(new Map<number, string>())
  // 窄屏抽屉的开合。桌面（lg 起）用常驻双栏/左侧目录；<1024px 没有空间，
  // 改成两个 Sheet——按钮在窄屏上仍然可见，点了必须有"看得见的结果"。
  const [outlineOpen, setOutlineOpen] = useState(false)
  const [translationOpen, setTranslationOpen] = useState(false)

  const translation = translations[pageNo] ?? null
  const pageText = pageTexts[pageNo] ?? null
  const pageTextLoading = bilingual && pageText === null
  // bbox 只在"深链进来的那一页"画：翻页后自动失效（citationPage 为 null 时永不画）。
  const citationBBox = bboxStyle(pageNo === citationPage ? bbox : null)

  // 章节目录（科研助手 M3）：点击某节直接把页码切过去。
  //
  // 【本地再声明一次形状，而不是依赖 Schemas】useQuery 的返回类型在这里
  // 退化成 any（见 useDocuments.ts 里的说明），而 JSX 的 map 回调参数是
  // any 时 tsc 会报 noImplicitAny。这里把数据形状钉在消费侧最直接。
  const { data: outlineData } = useDocumentOutline(id)
  const outline: OutlineRow[] | undefined = outlineData
  // 【高亮计算只做一次】原来是"每个目录项都 reduce 一遍"，O(n²)；
  // 现在二分 + useMemo，只在 outline/pageNo 变化时重算。
  const activeOutline = useMemo(
    () => (outline ? activeOutlineIndex(outline, pageNo) : -1),
    [outline, pageNo],
  )
  // 引用图谱（M5）：参考文献列表 + 库内命中标记。
  const { data: references } = useDocumentReferences(id)

  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  // 双语对照的两栏滚动同步：原文栏 ↔ 译文栏按滚动比例联动（官方 M3 要求
  // “中英对比阅读模式，两侧页面拖动时自动同步滚动”）。
  const originalTextRef = useRef<HTMLParagraphElement | null>(null)
  const translationTextRef = useRef<HTMLParagraphElement | null>(null)
  const syncingScroll = useRef(false)

  const syncScroll = useCallback((from: HTMLElement | null, to: HTMLElement | null) => {
    if (!from || !to || syncingScroll.current) return
    const fromRange = from.scrollHeight - from.clientHeight
    const toRange = to.scrollHeight - to.clientHeight
    if (fromRange <= 0 || toRange <= 0) return
    syncingScroll.current = true
    to.scrollTop = (from.scrollTop / fromRange) * toRange
    requestAnimationFrame(() => {
      syncingScroll.current = false
    })
  }, [])

  // 文档元信息：文件名、页数、回到知识库的链接。
  useEffect(() => {
    let cancelled = false
    // 走契约客户端而不是手写 fetch：失败抛出来的是 RFC 7807 Problem 对象，
    // 下面的 ErrorText 才能按 type 给出中文说明（手写 fetch 抛的 Error 没有 type，
    // 用户看到的是后端 JSON 原文）。Document 结构上覆盖了本页要用的 DocumentMeta。
    api
      .GET('/api/v1/documents/{id}', { params: { path: { id } } })
      .then((result) => unwrap(result))
      .then((m) => {
        if (!cancelled) setMeta(m)
      })
      .catch((e) => {
        if (!cancelled) setMetaError(e)
      })
    return () => {
      cancelled = true
    }
  }, [id])

  // 加载 PDF：url 是后端的原始文件端点（inline application/pdf）。
  //
  // 【worker 为什么每次挂载新建，而不是模块级单例】PDFDocumentLoadingTask.destroy()
  // 会连带销毁 workerPort——这是实测出来的（R4 截图时 React StrictMode 的
  // 二次挂载直接抛 "PDFWorker.create - the worker is being destroyed"）。
  // 单例写法下，**离开阅读器再进来**（生产环境也会发生）就会白屏报错；
  // 每份文档一个新 worker 是几毫秒开销，换的是"第二次打开还能用"。
  useEffect(() => {
    let cancelled = false
    const worker = new PdfWorker()
    pdfjs.GlobalWorkerOptions.workerPort = worker
    const task = pdfjs.getDocument({ url: `/api/v1/documents/${id}/file` })
    task.promise
      .then((doc) => {
        if (cancelled) return
        setPdf(doc)
        setPageNo((p) => Math.min(Math.max(1, p), doc.numPages))
      })
      .catch((e) => {
        if (!cancelled) setLoadError(e)
      })
    return () => {
      cancelled = true
      void task.destroy()
      worker.terminate()
    }
  }, [id])

  // 渲染当前页。devicePixelRatio 用来出高分屏不糊的图：canvas 的位图尺寸
  // 乘以 dpr、CSS 尺寸保持逻辑像素，浏览器缩回去刚好清晰。
  useEffect(() => {
    if (!pdf || !canvasRef.current) return
    let cancelled = false
    // 【为什么先 cancel 再开新的】PDF.js 不允许同一张 canvas 上并发两次
    // render()（抛 "Cannot use the same canvas during multiple render()
    // operations"）。连续翻页/连点缩放时两次 effect 会重叠，不取消旧任务的话
    // 这个异常会落进 loadError，用户看到的是"PDF 加载失败"——而 PDF 其实完好。
    // 顺带取消也让"快速翻 10 页"只真正画最后一页。
    renderTaskRef.current?.cancel()
    renderTaskRef.current = null
    setRendering(true)
    ;(async () => {
      try {
        const page = await pdf.getPage(pageNo)
        const viewport = page.getViewport({ scale })
        const canvas = canvasRef.current
        if (!canvas || cancelled) return
        const dpr = window.devicePixelRatio || 1
        canvas.width = Math.floor(viewport.width * dpr)
        canvas.height = Math.floor(viewport.height * dpr)
        canvas.style.width = `${Math.floor(viewport.width)}px`
        canvas.style.height = `${Math.floor(viewport.height)}px`
        const ctx = canvas.getContext('2d')
        if (!ctx) return
        const task = page.render({
          canvas,
          canvasContext: ctx,
          viewport,
          transform: dpr === 1 ? undefined : [dpr, 0, 0, dpr, 0, 0],
        })
        renderTaskRef.current = task
        await task.promise
      } catch (e) {
        if (cancelled) return
        // 取消是预期行为（翻页/缩放/卸载），不是加载失败，不要显示错误页。
        if (isRenderCancelled(e)) return
        setLoadError(e)
      } finally {
        if (!cancelled) setRendering(false)
      }
    })()
    return () => {
      cancelled = true
      renderTaskRef.current?.cancel()
      renderTaskRef.current = null
    }
  }, [pdf, pageNo, scale])

  // 悬停翻译：取鼠标下的句子，450ms 防抖后请求翻译；同一句只请求一次。
  const hoverTextRef = useRef('')
  const clearHover = useCallback(() => {
    if (hoverTimer.current !== null) {
      window.clearTimeout(hoverTimer.current)
      hoverTimer.current = null
    }
    hoverTextRef.current = ''
    setHover(null)
  }, [])

  // 【翻页前先关浮层】浮层是 fixed 坐标、绑在"打开那一刻的鼠标位置"上；
  // 翻页后那些坐标指向的是新页面里的无关内容。go() 里统一清掉。
  const go = useCallback(
    (next: number) => {
      if (!pdf) return
      clearHover()
      setPageNo(Math.min(Math.max(1, next), pdf.numPages))
    },
    [pdf, clearHover],
  )

  // 【滚动时也要关】阅读区自己是滚动容器，scroll 事件不冒泡到 window，
  // 所以用 capture 捕；滚过去之后浮层不跟手，留着只会指错位置。
  useEffect(() => {
    if (!hover) return
    const close = () => clearHover()
    window.addEventListener('scroll', close, true)
    return () => window.removeEventListener('scroll', close, true)
  }, [hover, clearHover])

  // 抽取某一页的文本（带缓存）。PDF.js 的 textContent 是"文本片段"数组，
  // 直接 join 会丢词间空格，所以用空格拼、再压掉连续空白。
  const loadPageText = useCallback(
    async (pageNumber: number): Promise<string> => {
      const cached = pageTextCache.current.get(pageNumber)
      if (cached !== undefined) return cached
      if (!pdf) return ''
      const page = await pdf.getPage(pageNumber)
      const content = await page.getTextContent()
      const text = content.items
        .map((item) => ('str' in item ? item.str : ''))
        .join(' ')
        .replace(/\s+/g, ' ')
        .trim()
        .slice(0, 8000)
      pageTextCache.current.set(pageNumber, text)
      return text
    },
    [pdf],
  )

  // 当前页文本：双语对照打开时自动抽出来，翻页则跟着换页。
  //
  // 【为什么不在 effect 里同步 setState】react-hooks 的 set-state-in-effect
  // 会把"effect 里同步 setLoading(true)"判成级联渲染；这里只在 await 之后
  // 落文本，loading 状态由派生值 pageTextLoading 表达。
  useEffect(() => {
    if (!bilingual || !pdf) return
    let cancelled = false
    loadPageText(pageNo)
      .then((text) => {
        if (!cancelled) setPageTexts((prev) => (prev[pageNo] === text ? prev : { ...prev, [pageNo]: text }))
      })
      .catch((e) => {
        if (!cancelled) setTranslateError(e)
      })
    return () => {
      cancelled = true
    }
  }, [bilingual, pageNo, pdf, loadPageText])

  // 提取当前页文本并翻译。结果按页缓存，双语对照翻页时可直接复用。
  const translatePage = useCallback(async () => {
    if (!pdf) return
    setTranslating(true)
    setTranslateError(null)
    try {
      const text = await loadPageText(pageNo)
      if (!text) throw new Error('这一页没有可提取的文字（可能是纯图片页）')
      const data = unwrap(
        await api.POST('/api/v1/translate', { body: { text, targetLang } }),
      )
      setTranslations((prev) => ({ ...prev, [pageNo]: data.translation }))
    } catch (e) {
      setTranslateError(e)
    } finally {
      setTranslating(false)
    }
  }, [pdf, pageNo, loadPageText, targetLang])

  // 划词翻译：在双语对照的"原文"栏里选中一段，自动请求该段的译文。
  const handleSelectionTranslate = useCallback(async () => {
    const text = window.getSelection()?.toString().trim() ?? ''
    if (text.length < 2) return
    const clipped = text.slice(0, 600)
    setSelection({ text: clipped, translation: null, loading: true })
    try {
      const data = unwrap(
        await api.POST('/api/v1/translate', { body: { text: clipped, targetLang } }),
      )
      setSelection({ text: clipped, translation: data.translation, loading: false })
    } catch {
      setSelection({ text: clipped, translation: '翻译失败，请重试', loading: false })
    }
  }, [targetLang])

  // 切换翻译目标语言。桌面 select 与窄屏抽屉共用：缓存按页只存一种语言，
  // 切换后必须清空，否则会把上一次语言的译文当成本次的展示出来。
  const changeTargetLang = useCallback((next: 'zh-CN' | 'en') => {
    setTargetLang(next)
    setTranslations({})
    setSelection(null)
  }, [])

  // 悬停翻译：句子切分在 lib/reader.ts（纯函数、有边界用例）；这里只负责
  // 取鼠标下的文本节点、防抖和请求。
  const handleHoverMove = useCallback((e: ReactMouseEvent<HTMLParagraphElement>) => {
    const selected = window.getSelection()?.toString().trim() ?? ''
    if (selected.length >= 2) return
    const range = (
      document as Document & {
        caretRangeFromPoint?: (x: number, y: number) => Range | null
      }
    ).caretRangeFromPoint?.(e.clientX, e.clientY)
    const node = range?.startContainer
    if (!node || node.nodeType !== Node.TEXT_NODE) return
    const full = node.textContent ?? ''
    const offset = range?.startOffset ?? 0
    const sentence = splitSentences(full, offset).slice(0, 300)
    if (sentence.length < 4 || sentence === hoverTextRef.current) return
    hoverTextRef.current = sentence
    const { clientX: x, clientY: y } = e
    if (hoverTimer.current !== null) window.clearTimeout(hoverTimer.current)
    hoverTimer.current = window.setTimeout(() => {
      const cached = hoverCache.current.get(sentence)
      if (cached) {
        setHover({ x, y, text: sentence, translation: cached })
        return
      }
      setHover({ x, y, text: sentence, translation: null })
      void api
        .POST('/api/v1/translate', { body: { text: sentence, targetLang } })
        .then((result) => unwrap(result))
        .then((data) => {
          hoverCache.current.set(sentence, data.translation)
          setHover((prev) => (prev?.text === sentence ? { ...prev, translation: data.translation } : prev))
        })
        .catch(() => {
          setHover((prev) => (prev?.text === sentence ? { ...prev, translation: '翻译失败，请重试' } : prev))
        })
    }, 450)
  }, [targetLang])

  // 错误态统一在下面渲染：页面只能有一个 <h1>（headingStructure.test.ts 钉着
  // 这条规范），所以不能每个分支各写一个 return + h1。
  const errorBlock = metaError ? (
    <div className="mx-auto max-w-2xl p-6">
      <Alert variant="destructive">
        <AlertTitle>读不到文档信息</AlertTitle>
        <AlertDescription>
          <ErrorText error={metaError} />
        </AlertDescription>
      </Alert>
    </div>
  ) : loadError ? (
    <div className="mx-auto max-w-2xl p-6">
      <Alert variant="destructive">
        <AlertTitle>PDF 加载失败</AlertTitle>
        <AlertDescription>
          <ErrorText error={loadError} />
          <p className="mt-2 text-xs">如果是刚上传的文档，等状态变成「就绪」再试。</p>
        </AlertDescription>
      </Alert>
    </div>
  ) : null

  return (
    <div className="flex h-full min-h-0 flex-col">
      <h1 className="sr-only">论文阅读器：{meta?.filename ?? '加载中'}</h1>

      {errorBlock}
      {errorBlock ? null : (
        <>
          {/* 工具栏：文件名 / 页码 / 缩放 / 回知识库 */}
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">{meta?.filename ?? '加载中…'}</div>
          <div className="text-muted-foreground text-xs">
            {meta ? `${meta.pageCount || '?'} 页 · ${meta.chunkCount} 个分块` : '\u00a0'}
            {section ? <span className="ml-2">· {section}</span> : null}
          </div>
        </div>

        <div className="ml-auto flex items-center gap-1">
          <Button
            variant="outline"
            size="icon"
            aria-label="上一页"
            disabled={pageNo <= 1}
            onClick={() => go(pageNo - 1)}
          >
            <ChevronLeft />
          </Button>
          <span className="min-w-20 text-center text-sm">
            {pageNo} / {pdf?.numPages ?? '?'}
          </span>
          <Button
            variant="outline"
            size="icon"
            aria-label="下一页"
            disabled={!pdf || pageNo >= pdf.numPages}
            onClick={() => go(pageNo + 1)}
          >
            <ChevronRight />
          </Button>
          <Button
            variant="outline"
            size="icon"
            aria-label="缩小"
            onClick={() => setScale((s) => Math.max(0.6, +(s - 0.2).toFixed(1)))}
          >
            <ZoomOut />
          </Button>
          <Button
            variant="outline"
            size="icon"
            aria-label="放大"
            onClick={() => setScale((s) => Math.min(3, +(s + 0.2).toFixed(1)))}
          >
            <ZoomIn />
          </Button>
          {/* 窄屏专属：目录抽屉。桌面这一项在左侧常驻栏里，所以这里 lg 起隐藏。 */}
          <Button
            variant="outline"
            size="sm"
            className="lg:hidden"
            onClick={() => setOutlineOpen(true)}
          >
            <List /> 目录
          </Button>
          {/* 桌面：双语/翻译是右栏的开关。窄屏没有右栏，下面另有一组按钮
              打开抽屉——**同一个功能在不同宽度下用不同的壳，但不能一个可见、
              一个点了没反应**（R1 审计里记的"窄屏假按钮"就是后一种）。 */}
          <Button
            variant={bilingual ? 'default' : 'outline'}
            size="sm"
            className="hidden lg:inline-flex"
            aria-pressed={bilingual}
            onClick={() => {
              setBilingual((v) => !v)
              setTranslateError(null)
            }}
            disabled={!pdf}
          >
            <Languages /> {bilingual ? '关闭对照' : '双语对照'}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="hidden lg:inline-flex"
            onClick={() => void translatePage()}
            disabled={!pdf || translating}
          >
            <Languages /> {translating ? '翻译中…' : '翻译本页'}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="lg:hidden"
            onClick={() => {
              setBilingual(true)
              setTranslateError(null)
              setTranslationOpen(true)
            }}
            disabled={!pdf}
          >
            <Languages /> 双语对照
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="lg:hidden"
            onClick={() => {
              setTranslationOpen(true)
              void translatePage()
            }}
            disabled={!pdf || translating}
          >
            <Languages /> {translating ? '翻译中…' : '翻译本页'}
          </Button>
          {meta ? (
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/knowledge-bases/${meta.knowledgeBaseId}`}>
                <ExternalLink /> 知识库
              </Link>
            </Button>
          ) : null}
        </div>
      </div>

      <div className="flex min-h-0 flex-1">
        {/* 章节目录：解析阶段已经算好页码与层级，这里只负责渲染和跳页。
            窄屏没有它（见上面的目录按钮 + 底部 Sheet）。 */}
        <aside className="hidden w-60 shrink-0 overflow-auto border-r lg:block">
          <h2 className="text-muted-foreground px-3 py-2 text-xs font-medium">章节目录</h2>
          <OutlineList outline={outline} activeIndex={activeOutline} onGo={go} />

          {/* 引用图谱（M5）：参考文献 + 库内被引。
              命中的条目用主色标出——它表示这篇参考文献的作者版本也在
              当前知识库里，可以点过去看。 */}
          <h2 className="text-muted-foreground border-t px-3 py-2 text-xs font-medium">
            参考文献{references && references.length > 0 ? `（${references.length}）` : ''}
          </h2>
          <ReferencesList references={references} />
        </aside>

        {/* 画布区：滚动容器 + 居中页面。overlay 和 canvas 在同一个
            position:relative 的盒子里，按百分比定位，两者尺寸永远一致。 */}
        <div className="bg-muted/40 min-h-0 flex-1 overflow-auto p-6">
        {!pdf || rendering ? (
          // role=status：加载/渲染是"界面自己变了"，读屏用户需要被念一次
          // （README §13）。
          <div role="status" className="text-muted-foreground mb-3 text-center text-xs">
            {!pdf ? '正在加载 PDF…' : '正在渲染…'}
          </div>
        ) : null}
        <div className="flex justify-center">
          <div className="relative shadow-lg" style={{ lineHeight: 0 }}>
            {/* canvas 本身对读屏软件是一片空白：给它一个说明当前页的可访问名，
                让它至少能被当成"一张第 N 页的图"来播报，而不是无标签元素。 */}
            <canvas
              ref={canvasRef}
              className="block bg-white"
              role="img"
              aria-label={`PDF 第 ${pageNo} 页${pdf ? `，共 ${pdf.numPages} 页` : ''}`}
            />
            {citationBBox ? (
              <div
                className="pointer-events-none absolute border-2 border-amber-400 bg-amber-300/30"
                style={citationBBox}
              />
            ) : null}
          </div>
        </div>
        {!pdf && !loadError ? <PageFallback /> : null}
          </div>

          {/* 双语对照 / 翻译面板（桌面）。窄屏用底部两个 Sheet，见文件末尾。 */}
          {bilingual || translation !== null || translateError !== null ? (
            <aside className="hidden w-96 shrink-0 overflow-auto border-l p-4 lg:block">
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <h2 className="text-xs font-medium">
                    {bilingual ? '双语对照' : '译文'} · 第 {pageNo} 页
                  </h2>
                  <select
                    aria-label="翻译目标语言"
                    className="border-input bg-background rounded border px-1 py-0.5 text-xs"
                    value={targetLang}
                    onChange={(e) => changeTargetLang(e.target.value as 'zh-CN' | 'en')}
                  >
                    <option value="zh-CN">中文</option>
                    <option value="en">English</option>
                  </select>
                </div>
                <button
                  type="button"
                  aria-label="关闭翻译面板"
                  className="text-muted-foreground hover:text-foreground"
                  onClick={() => {
                    setBilingual(false)
                    setTranslateError(null)
                    setSelection(null)
                  }}
                >
                  <X className="size-4" />
                </button>
              </div>

              {bilingual ? (
                <div className="mt-2 space-y-4">
                  <div>
                    <div className="text-muted-foreground mb-1 flex items-center justify-between text-xs">
                      <span>原文（选中文字可划词翻译）</span>
                      {pageTextLoading ? <span>提取中…</span> : null}
                    </div>
                    <p
                      ref={originalTextRef}
                      className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap select-text"
                      onMouseUp={() => void handleSelectionTranslate()}
                      onMouseMove={handleHoverMove}
                      onMouseLeave={clearHover}
                      onScroll={() => syncScroll(originalTextRef.current, translationTextRef.current)}
                    >
                      {pageText ?? (pageTextLoading ? '正在提取本页文本…' : '本页没有可提取的文字')}
                    </p>
                  </div>

                  {selection ? <SelectionCard selection={selection} onClose={() => setSelection(null)} /> : null}

                  <div>
                    <div className="text-muted-foreground mb-1 flex items-center justify-between text-xs">
                      <span>译文</span>
                      <button
                        type="button"
                        className="text-primary hover:underline disabled:text-muted-foreground"
                        onClick={() => void translatePage()}
                        disabled={translating}
                      >
                        {translating ? '翻译中…' : translation ? '重新翻译' : '翻译本页'}
                      </button>
                    </div>
                    {translateError !== null ? (
                      <p className="text-destructive text-xs">
                        <ErrorText error={translateError} />
                      </p>
                    ) : (
                      <p
                        ref={translationTextRef}
                        className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap"
                        onScroll={() => syncScroll(translationTextRef.current, originalTextRef.current)}
                      >
                        {translation ?? '当前页还没有译文，点"翻译本页"生成。翻页时会自动复用已缓存的译文。'}
                      </p>
                    )}
                  </div>
                </div>
              ) : translateError !== null ? (
                <p className="text-destructive mt-2 text-xs">
                  <ErrorText error={translateError} />
                </p>
              ) : (
                <p className="mt-2 text-sm leading-relaxed whitespace-pre-wrap">{translation}</p>
              )}
            </aside>
          ) : null}
        </div>

        {/* 窄屏「目录」抽屉。与左侧 aside 共用 OutlineList/ReferencesList，
            保证两处渲染的是同一份数据、同一套空态文案。 */}
        <Sheet open={outlineOpen} onOpenChange={setOutlineOpen}>
          <SheetContent side="left" className="w-[92vw] max-w-sm overflow-y-auto">
            <SheetHeader>
              <SheetTitle>章节目录</SheetTitle>
            </SheetHeader>
            <div className="px-4 pb-6">
              <OutlineList
                outline={outline}
                activeIndex={activeOutline}
                onGo={(page) => {
                  go(page)
                  setOutlineOpen(false)
                }}
              />
              <h3 className="text-muted-foreground border-t px-3 py-2 text-xs font-medium">
                参考文献{references && references.length > 0 ? `（${references.length}）` : ''}
              </h3>
              <ReferencesList references={references} />
            </div>
          </SheetContent>
        </Sheet>

        {/* 窄屏「翻译」抽屉。桌面是左右双栏 + 滚动比例联动；窄屏放不下双栏，
            改成上下排列的同一份内容（不做滚动同步——两栏都变成上下滚动的
            同一列时，同步没有意义）。 */}
        <Sheet open={translationOpen} onOpenChange={setTranslationOpen}>
          <SheetContent side="right" className="w-[92vw] max-w-md overflow-y-auto">
            <SheetHeader>
              <SheetTitle>
                {bilingual ? '双语对照' : '译文'} · 第 {pageNo} 页
              </SheetTitle>
              <SheetDescription className="sr-only">
                窄屏下原文与译文上下排列；桌面为左右双栏且滚动同步。
              </SheetDescription>
            </SheetHeader>
            <div className="space-y-4 px-4 pb-6">
              <div className="flex flex-wrap items-center gap-2">
                <label className="text-muted-foreground text-xs" htmlFor="reader-target-lang">
                  目标语言
                </label>
                <select
                  id="reader-target-lang"
                  aria-label="翻译目标语言"
                  className="border-input bg-background rounded border px-1 py-0.5 text-xs"
                  value={targetLang}
                  onChange={(e) => changeTargetLang(e.target.value as 'zh-CN' | 'en')}
                >
                  <option value="zh-CN">中文</option>
                  <option value="en">English</option>
                </select>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void translatePage()}
                  disabled={translating}
                >
                  {translating ? '翻译中…' : translation ? '重新翻译' : '翻译本页'}
                </Button>
              </div>

              {bilingual ? (
                <>
                  <section>
                    <h2 className="text-muted-foreground mb-1 text-xs font-medium">
                      原文（选中文字可划词翻译）
                    </h2>
                    <p
                      className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap select-text"
                      onMouseUp={() => void handleSelectionTranslate()}
                    >
                      {pageText ?? (pageTextLoading ? '正在提取本页文本…' : '本页没有可提取的文字')}
                    </p>
                  </section>
                  {selection ? (
                    <SelectionCard selection={selection} onClose={() => setSelection(null)} />
                  ) : null}
                  <section>
                    <h2 className="text-muted-foreground mb-1 text-xs font-medium">译文</h2>
                    {translateError !== null ? (
                      <p className="text-destructive text-xs">
                        <ErrorText error={translateError} />
                      </p>
                    ) : (
                      <p className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap">
                        {translation ?? '当前页还没有译文，点"翻译本页"生成。'}
                      </p>
                    )}
                  </section>
                </>
              ) : translateError !== null ? (
                <p className="text-destructive text-xs">
                  <ErrorText error={translateError} />
                </p>
              ) : (
                <p className="text-sm leading-relaxed whitespace-pre-wrap">
                  {translation ?? '当前页还没有译文，点"翻译本页"生成。'}
                </p>
              )}
            </div>
          </SheetContent>
        </Sheet>

        {hover ? (
          <div
            className="bg-popover text-popover-foreground fixed z-50 w-80 rounded-lg border p-2 text-xs shadow-lg"
            style={{
              left: Math.min(hover.x + 12, window.innerWidth - 340),
              top: Math.min(hover.y + 16, window.innerHeight - 120),
            }}
          >
            <p className="text-muted-foreground line-clamp-2">{hover.text}</p>
            <p className="mt-1 leading-relaxed">{hover.translation ?? '翻译中…'}</p>
          </div>
        ) : null}
      </>
    )}
    </div>
  )
}

/**
 * 章节目录列表（桌面左侧栏与窄屏抽屉共用）。
 *
 * 【为什么把空态/加载态也放进来】两处调用都要回答"没有目录时显示什么"；
 * 各自写一遍迟早会一处说"没有章节"、另一处说"加载中"。
 */
function OutlineList({
  outline,
  activeIndex,
  onGo,
}: {
  outline: OutlineRow[] | undefined
  activeIndex: number
  onGo: (page: number) => void
}) {
  if (!outline || outline.length === 0) {
    return (
      <p className="text-muted-foreground px-3 pb-4 text-xs">
        {outline ? '这份文档没有解析出章节标题' : '加载中…'}
      </p>
    )
  }

  return (
    <ul className="pb-4">
      {outline.map((entry, index) => (
        <li key={`${entry.page}-${index}`}>
          <button
            type="button"
            onClick={() => onGo(entry.page)}
            className={
              'block w-full truncate px-3 py-1 text-left text-xs hover:bg-muted ' +
              (index === activeIndex ? 'text-primary font-medium' : 'text-muted-foreground')
            }
            style={{ paddingLeft: 12 + (entry.level - 1) * 14 }}
            title={entry.title}
          >
            {entry.title}
          </button>
        </li>
      ))}
    </ul>
  )
}

/** 参考文献列表（桌面左侧栏与窄屏抽屉共用）。 */
function ReferencesList({ references }: { references: ReferenceList | undefined }) {
  if (!references || references.length === 0) {
    return (
      <p className="text-muted-foreground px-3 pb-4 text-xs">
        {references ? '没有解析出参考文献' : '加载中…'}
      </p>
    )
  }

  return (
    <ul className="pb-4">
      {references.map((ref, index) => (
        <li key={(ref.index || 0) + '-' + index} className="px-3 py-1">
          {ref.matchedDocumentId ? (
            <a
              href={`/documents/${ref.matchedDocumentId}/reader?page=1`}
              target="_blank"
              rel="noreferrer"
              className="text-primary block truncate text-xs hover:underline"
              title={ref.text + '（库内：' + (ref.matchedFilename ?? '') + '）'}
            >
              [{ref.index || index + 1}] {ref.text}
            </a>
          ) : (
            <span
              className="text-muted-foreground block truncate text-xs"
              title={ref.text + (ref.page > 0 ? `（第 ${ref.page} 页）` : '')}
            >
              [{ref.index || index + 1}] {ref.text}
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}

/** 划词翻译的结果卡片（桌面双栏与窄屏抽屉共用）。 */
function SelectionCard({
  selection,
  onClose,
}: {
  selection: { text: string; translation: string | null; loading: boolean }
  onClose: () => void
}) {
  return (
    <div className="border-primary/40 bg-primary/5 rounded-lg border p-2">
      <div className="text-muted-foreground mb-1 flex items-center justify-between text-xs">
        <span>划词翻译</span>
        <button type="button" className="hover:text-foreground" onClick={onClose}>
          关闭
        </button>
      </div>
      <p className="text-muted-foreground text-xs">{selection.text}</p>
      <p className="mt-1 text-sm leading-relaxed">
        {selection.loading ? '翻译中…' : selection.translation}
      </p>
    </div>
  )
}
