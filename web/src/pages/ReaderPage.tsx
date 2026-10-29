import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import * as pdfjs from 'pdfjs-dist'
import type { PDFDocumentProxy } from 'pdfjs-dist'
// 【为什么用 ?worker 而不是 ?url】?url 会产出独立的 .mjs 资源，交给后端
// 静态服务时 MIME 由 Go 的 mime 表猜；猜错（text/plain）会被浏览器的
// module script 校验直接拒绝。?worker 让 Vite 走自己的 worker 打包管线，
// 产物是标准 .js + new Worker(...)，不依赖服务端 MIME 判断。
import PdfWorker from 'pdfjs-dist/build/pdf.worker.min.mjs?worker'
import { ChevronLeft, ChevronRight, ExternalLink, ZoomIn, ZoomOut } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ErrorText } from '@/components/ErrorText'
import { PageFallback } from '@/components/PageFallback'

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
          {meta ? (
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/knowledge-bases/${meta.knowledgeBaseId}`}>
                <ExternalLink /> 知识库
              </Link>
            </Button>
          ) : null}
        </div>
      </div>

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
        </>
      )}
    </div>
  )
}
