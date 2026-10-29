import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import * as pdfjs from 'pdfjs-dist'
import type { PDFDocumentProxy } from 'pdfjs-dist'
// 【为什么用 ?worker 而不是 ?url】?url 会产出独立的 .mjs 资源，交给后端
// 静态服务时 MIME 由 Go 的 mime 表猜；猜错（text/plain）会被浏览器的
// module script 校验直接拒绝。?worker 让 Vite 走自己的 worker 打包管线，
// 产物是标准 .js + new Worker(...)，不依赖服务端 MIME 判断。
import PdfWorker from 'pdfjs-dist/build/pdf.worker.min.mjs?worker'
import { ChevronLeft, ChevronRight, ExternalLink, Languages, X, ZoomIn, ZoomOut } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ErrorText } from '@/components/ErrorText'
import { PageFallback } from '@/components/PageFallback'
import { useDocumentOutline, useDocumentReferences } from '@/hooks/useDocuments'

// PDF.js 的 worker 必须显式指定。workerPort 接一个真的 Worker 实例，
// 比 workerSrc 再让 pdf.js 自己按 URL 去 import 更可控（见上面的 ?worker 说明）。
pdfjs.GlobalWorkerOptions.workerPort = new PdfWorker()

/** 文档元信息（本页只用到这几项，不引 api-client 的完整类型）。 */
type DocumentMeta = {
  id: string
  knowledgeBaseId: string
  filename: string
  pageCount: number
  chunkCount: number
  status: string
}

type BBox = [number, number, number, number]

/** 从 query 里解析归一化 bbox（"x0,y0,x1,y1"），非法值当没有。 */
function parseBBox(raw: string | null): BBox | null {
  if (!raw) return null
  const parts = raw.split(',').map(Number)
  if (parts.length !== 4 || parts.some((v) => Number.isNaN(v))) return null
  const [x0, y0, x1, y1] = parts
  if (x1 <= x0 || y1 <= y0) return null
  return [x0, y0, x1, y1]
}

/**
 * 论文阅读器（参赛版）——点 citation 的「在原文中查看」跳到这里。
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

  return <ReaderView key={id} id={id} initialPage={initialPage} section={section} bbox={bbox} />
}

function ReaderView({
  id,
  initialPage,
  section,
  bbox,
}: {
  id: string
  initialPage: number
  section: string | null
  bbox: BBox | null
}) {
  const [meta, setMeta] = useState<DocumentMeta | null>(null)
  const [metaError, setMetaError] = useState<unknown>(null)
  const [pdf, setPdf] = useState<PDFDocumentProxy | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [pageNo, setPageNo] = useState(initialPage)
  const [scale, setScale] = useState(1.4)
  const [rendering, setRendering] = useState(false)
  // 翻译面板（参赛版 M3）：把当前页文本抽出来交给后端，用当前 chat 模型翻译。
  const [translating, setTranslating] = useState(false)
  // 按页保存翻译与原文（state 而不是 ref：翻页/切双语时要触发渲染）。
  const [translations, setTranslations] = useState<Record<number, string>>({})
  const [pageTexts, setPageTexts] = useState<Record<number, string>>({})
  const [translateError, setTranslateError] = useState<unknown>(null)
  // 双语对照（参赛版 M3）：左 PDF / 右"原文 + 译文"双栏；翻页时两栏一起切到
  // 该页（页级联动）。划词翻译在原文栏选中文字触发。
  const [bilingual, setBilingual] = useState(false)
  const [selection, setSelection] = useState<{
    text: string
    translation: string | null
    loading: boolean
  } | null>(null)
  // 原文抽取缓存（去重网络/解析）；翻译/原文的展示值由上面的 state 派生。
  const pageTextCache = useRef(new Map<number, string>())

  const translation = translations[pageNo] ?? null
  const pageText = pageTexts[pageNo] ?? null
  const pageTextLoading = bilingual && pageText === null

  // 章节目录（参赛版 M3）：点击某节直接把页码切过去。
  //
  // 【本地再声明一次形状，而不是依赖 Schemas】useQuery 的返回类型在这里
  // 退化成 any（见 useDocuments.ts 里的说明），而 JSX 的 map 回调参数是
  // any 时 tsc 会报 noImplicitAny。这里把数据形状钉在消费侧最直接。
  type OutlineRow = { title: string; page: number; level: number }
  const { data: outlineData } = useDocumentOutline(id)
  const outline: OutlineRow[] | undefined = outlineData
  // 引用图谱（M5）：参考文献列表 + 库内命中标记。
  const { data: references } = useDocumentReferences(id)

  const canvasRef = useRef<HTMLCanvasElement | null>(null)

  // 文档元信息：文件名、页数、回到知识库的链接。
  useEffect(() => {
    let cancelled = false
    fetch(`/api/v1/documents/${id}`)
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return (await r.json()) as DocumentMeta
      })
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
  useEffect(() => {
    let cancelled = false
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
    }
  }, [id])

  // 渲染当前页。devicePixelRatio 用来出高分屏不糊的图：canvas 的位图尺寸
  // 乘以 dpr、CSS 尺寸保持逻辑像素，浏览器缩回去刚好清晰。
  useEffect(() => {
    if (!pdf || !canvasRef.current) return
    let cancelled = false
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
        await page.render({
          canvas,
          canvasContext: ctx,
          viewport,
          transform: dpr === 1 ? undefined : [dpr, 0, 0, dpr, 0, 0],
        }).promise
      } catch (e) {
        if (!cancelled) setLoadError(e)
      } finally {
        if (!cancelled) setRendering(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [pdf, pageNo, scale])

  const go = useCallback(
    (next: number) => {
      if (!pdf) return
      setPageNo(Math.min(Math.max(1, next), pdf.numPages))
    },
    [pdf],
  )

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
      const res = await fetch('/api/v1/translate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text, targetLang: 'zh-CN' }),
      })
      if (!res.ok) throw new Error('HTTP ' + res.status)
      const data = (await res.json()) as { translation: string }
      setTranslations((prev) => ({ ...prev, [pageNo]: data.translation }))
    } catch (e) {
      setTranslateError(e)
    } finally {
      setTranslating(false)
    }
  }, [pdf, pageNo, loadPageText])

  // 划词翻译：在双语对照的"原文"栏里选中一段，自动请求该段的译文。
  const handleSelectionTranslate = useCallback(async () => {
    const text = window.getSelection()?.toString().trim() ?? ''
    if (text.length < 2) return
    const clipped = text.slice(0, 600)
    setSelection({ text: clipped, translation: null, loading: true })
    try {
      const res = await fetch('/api/v1/translate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: clipped, targetLang: 'zh-CN' }),
      })
      if (!res.ok) throw new Error('HTTP ' + res.status)
      const data = (await res.json()) as { translation: string }
      setSelection({ text: clipped, translation: data.translation, loading: false })
    } catch {
      setSelection({ text: clipped, translation: '翻译失败，请重试', loading: false })
    }
  }, [])

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
          <Button
            variant={bilingual ? 'default' : 'outline'}
            size="sm"
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
            onClick={() => void translatePage()}
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
        {/* 章节目录：解析阶段已经算好页码与层级，这里只负责渲染和跳页。 */}
        <aside className="hidden w-60 shrink-0 overflow-auto border-r lg:block">
          <div className="text-muted-foreground px-3 py-2 text-xs font-medium">章节目录</div>
          {outline && outline.length > 0 ? (
            <ul className="pb-4">
              {outline.map((entry, index) => {
                // 当前页落在哪一节：取"页码 ≤ 当前页"的最后一个条目。
                const active = outline.reduce(
                  (acc, cur, i) => (cur.page <= pageNo ? i : acc),
                  -1,
                )
                return (
                  <li key={index}>
                    <button
                      type="button"
                      onClick={() => go(entry.page)}
                      className={
                        'block w-full truncate px-3 py-1 text-left text-xs hover:bg-neutral-100 dark:hover:bg-neutral-800 ' +
                        (index === active ? 'text-primary font-medium' : 'text-muted-foreground')
                      }
                      style={{ paddingLeft: 12 + (entry.level - 1) * 14 }}
                      title={entry.title}
                    >
                      {entry.title}
                    </button>
                  </li>
                )
              })}
            </ul>
          ) : (
            <p className="text-muted-foreground px-3 text-xs">
              {outline ? '这份文档没有解析出章节标题' : '加载中…'}
            </p>
          )}

          {/* 引用图谱（M5）：参考文献 + 库内被引。
              命中的条目用主色标出——它表示这篇参考文献的作者版本也在
              当前知识库里，可以点过去看。 */}
          <div className="text-muted-foreground border-t px-3 py-2 text-xs font-medium">
            参考文献{references && references.length > 0 ? `（${references.length}）` : ''}
          </div>
          {references && references.length > 0 ? (
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
          ) : (
            <p className="text-muted-foreground px-3 pb-4 text-xs">
              {references ? '没有解析出参考文献' : '加载中…'}
            </p>
          )}
        </aside>

        {/* 画布区：滚动容器 + 居中页面。overlay 和 canvas 在同一个
            position:relative 的盒子里，按百分比定位，两者尺寸永远一致。 */}
        <div className="min-h-0 flex-1 overflow-auto bg-neutral-100 p-6 dark:bg-neutral-900">
        {!pdf || rendering ? (
          <div className="mb-3 text-center text-xs text-neutral-500">
            {!pdf ? '正在加载 PDF…' : '正在渲染…'}
          </div>
        ) : null}
        <div className="flex justify-center">
          <div className="relative shadow-lg" style={{ lineHeight: 0 }}>
            <canvas ref={canvasRef} className="block bg-white" />
            {bbox ? (
              <div
                className="pointer-events-none absolute border-2 border-amber-400 bg-amber-300/30"
                style={{
                  left: `${bbox[0] * 100}%`,
                  top: `${bbox[1] * 100}%`,
                  width: `${(bbox[2] - bbox[0]) * 100}%`,
                  height: `${(bbox[3] - bbox[1]) * 100}%`,
                }}
              />
            ) : null}
          </div>
        </div>
        {!pdf && !loadError ? <PageFallback /> : null}
          </div>

          {/* 双语对照 / 翻译面板。
              bilingual=true 时右侧固定显示"原文 + 译文"：翻页两栏按页联动，
              原文可划词（选中自动翻译）；只点"翻译本页"时退化为单译文面板。
              失败留在面板里（读操作，不弹 toast）。 */}
          {bilingual || translation !== null || translateError !== null ? (
            <aside className="hidden w-96 shrink-0 overflow-auto border-l p-4 lg:block">
              <div className="flex items-center justify-between gap-2">
                <div className="text-xs font-medium">
                  {bilingual ? '双语对照' : '译文'} · 第 {pageNo} 页
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
                      className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap select-text"
                      onMouseUp={() => void handleSelectionTranslate()}
                    >
                      {pageText ?? (pageTextLoading ? '正在提取本页文本…' : '本页没有可提取的文字')}
                    </p>
                  </div>

                  {selection ? (
                    <div className="border-primary/40 bg-primary/5 rounded-lg border p-2">
                      <div className="text-muted-foreground mb-1 flex items-center justify-between text-xs">
                        <span>划词翻译</span>
                        <button
                          type="button"
                          className="hover:text-foreground"
                          onClick={() => setSelection(null)}
                        >
                          关闭
                        </button>
                      </div>
                      <p className="text-muted-foreground text-xs">{selection.text}</p>
                      <p className="mt-1 text-sm leading-relaxed">
                        {selection.loading ? '翻译中…' : selection.translation}
                      </p>
                    </div>
                  ) : null}

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
                      <p className="text-xs text-red-600">
                        <ErrorText error={translateError} />
                      </p>
                    ) : (
                      <p className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap">
                        {translation ?? '当前页还没有译文，点"翻译本页"生成。翻页时会自动复用已缓存的译文。'}
                      </p>
                    )}
                  </div>
                </div>
              ) : translateError !== null ? (
                <p className="mt-2 text-xs text-red-600">
                  <ErrorText error={translateError} />
                </p>
              ) : (
                <p className="mt-2 text-sm leading-relaxed whitespace-pre-wrap">{translation}</p>
              )}
            </aside>
          ) : null}
        </div>
      </>
    )}
    </div>
  )
}
