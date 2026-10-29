import { useState } from 'react'
import { BarChart3, Copy, Table2 } from 'lucide-react'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useVisualizeCSV } from '@/hooks/useDocuments'

const COLORS = ['#2563eb', '#f59e0b', '#10b981', '#ef4444', '#8b5cf6']

const SAMPLE_CSV = `模型,BLEU,参数量(B)
Transformer,28.4,65
BERT,24.1,110
GPT-2,29.1,1542
GPT-3,31.5,175000`

/** 简单柱状/折线/雷达图：不引图表库，用 SVG 画，够论文示意图用。 */
function ChartPreview({
  type,
  categories,
  series,
}: {
  type: 'line' | 'bar' | 'radar'
  categories: string[]
  series: { name: string; values: number[] }[]
}) {
  const W = 640
  const H = 320
  const PAD = 44
  const max = Math.max(1e-9, ...series.flatMap((s) => s.values))
  const innerW = W - PAD * 2
  const innerH = H - PAD * 2

  if (type === 'radar') {
    const n = Math.max(categories.length, 3)
    const cx = W / 2
    const cy = H / 2 + 10
    const r = Math.min(innerW, innerH) / 2
    const point = (i: number, v: number) => {
      const angle = (2 * Math.PI * i) / n - Math.PI / 2
      const rr = (v / max) * r
      return [cx + rr * Math.cos(angle), cy + rr * Math.sin(angle)]
    }
    return (
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full">
        {[0.25, 0.5, 0.75, 1].map((f) => (
          <polygon
            key={f}
            points={categories.map((_, i) => point(i, max * f).join(',')).join(' ')}
            className="fill-none stroke-border"
          />
        ))}
        {categories.map((c, i) => {
          const [x, y] = point(i, max)
          return (
            <text key={c} x={x} y={y} textAnchor="middle" className="fill-muted-foreground text-[10px]">
              {c.slice(0, 10)}
            </text>
          )
        })}
        {series.map((s, si) => (
          <polygon
            key={s.name}
            points={s.values.slice(0, n).map((v, i) => point(i, v).join(',')).join(' ')}
            fill={COLORS[si % COLORS.length]}
            fillOpacity={0.18}
            stroke={COLORS[si % COLORS.length]}
            strokeWidth={2}
          />
        ))}
      </svg>
    )
  }

  const step = innerW / Math.max(categories.length, 1)
  const barW = type === 'bar' ? Math.max(4, (step * 0.7) / Math.max(series.length, 1)) : 0
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full">
      <line x1={PAD} y1={H - PAD} x2={W - PAD} y2={H - PAD} className="stroke-border" />
      <line x1={PAD} y1={PAD} x2={PAD} y2={H - PAD} className="stroke-border" />
      <text x={PAD - 6} y={PAD - 8} textAnchor="end" className="fill-muted-foreground text-[10px]">
        {max.toFixed(2)}
      </text>
      {categories.map((c, i) => (
        <text
          key={c}
          x={PAD + step * i + step / 2}
          y={H - PAD + 16}
          textAnchor="middle"
          className="fill-muted-foreground text-[10px]"
        >
          {c.slice(0, 12)}
        </text>
      ))}
      {series.map((s, si) => {
        const color = COLORS[si % COLORS.length]
        if (type === 'bar') {
          return (
            <g key={s.name}>
              {s.values.map((v, i) => {
                const h = (v / max) * innerH
                return (
                  <rect
                    key={i}
                    x={PAD + step * i + step / 2 - (barW * series.length) / 2 + si * barW}
                    y={H - PAD - h}
                    width={barW * 0.9}
                    height={h}
                    fill={color}
                    rx={2}
                  />
                )
              })}
            </g>
          )
        }
        const points = s.values
          .map((v, i) => `${PAD + step * i + step / 2},${H - PAD - (v / max) * innerH}`)
          .join(' ')
        return (
          <g key={s.name}>
            <polyline points={points} fill="none" stroke={color} strokeWidth={2.5} />
            {s.values.map((v, i) => (
              <circle
                key={i}
                cx={PAD + step * i + step / 2}
                cy={H - PAD - (v / max) * innerH}
                r={3}
                fill={color}
              />
            ))}
          </g>
        )
      })}
    </svg>
  )
}

/**
 * 数据可视化 Copilot（参赛版 M6）：CSV → 表格 + 图表 + Mermaid/TikZ + 图注。
 */
export function KnowledgeVisualizePanel() {
  const [csv, setCsv] = useState(SAMPLE_CSV)
  const [title, setTitle] = useState('模型对比')
  const [chartType, setChartType] = useState<'auto' | 'line' | 'bar' | 'radar'>('auto')
  const [copied, setCopied] = useState<string | null>(null)
  const visualize = useVisualizeCSV()
  const result = visualize.data

  const copy = async (key: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(key)
      setTimeout(() => setCopied(null), 1500)
    } catch {
      setCopied(null)
    }
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-[1fr_auto_auto] sm:items-end">
        <div className="space-y-1.5">
          <Label htmlFor="csv-title">图标题</Label>
          <Input id="csv-title" value={title} onChange={(e) => setTitle(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="csv-type">图表类型</Label>
          <select
            id="csv-type"
            className="border-border bg-background h-9 rounded-md border px-2 text-sm"
            value={chartType}
            onChange={(e) => setChartType(e.target.value as typeof chartType)}
          >
            <option value="auto">自动</option>
            <option value="bar">柱状图</option>
            <option value="line">折线图</option>
            <option value="radar">雷达图</option>
          </select>
        </div>
        <Button onClick={() => visualize.mutate({ csv, title, chartType })} disabled={visualize.isPending}>
          <BarChart3 />
          {visualize.isPending ? '生成中…' : '生成图表'}
        </Button>
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="csv-input">CSV 数据（首行表头，数值列会被自动识别）</Label>
        <textarea
          id="csv-input"
          className="border-border bg-background h-32 w-full rounded-md border p-2 font-mono text-xs"
          value={csv}
          onChange={(e) => setCsv(e.target.value)}
        />
        <p className="text-muted-foreground text-xs">
          也可以直接从论文表格复制粘贴；生成后图表用 SVG 渲染，Mermaid/TikZ 代码可直接贴进论文。
        </p>
      </div>

      {visualize.error ? (
        <Alert variant="destructive">
          <AlertTitle>生成失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={visualize.error} />
          </AlertDescription>
        </Alert>
      ) : null}

      {result ? (
        <div className="space-y-4">
          <div className="border-border rounded-xl border p-4">
            <div className="mb-2 flex items-center justify-between gap-2">
              <div className="text-muted-foreground flex items-center gap-2 text-xs">
                <BarChart3 className="size-3.5" /> 图表预览（{result.chart.type}）
              </div>
              <div className="text-muted-foreground flex flex-wrap gap-2 text-xs">
                {result.chart.series.map((s, i) => (
                  <span key={s.name} className="flex items-center gap-1">
                    <span
                      className="inline-block size-2.5 rounded-full"
                      style={{ backgroundColor: COLORS[i % COLORS.length] }}
                    />
                    {s.name}
                  </span>
                ))}
              </div>
            </div>
            <ChartPreview
              type={result.chart.type}
              categories={result.chart.categories}
              series={result.chart.series}
            />
            <p className="text-muted-foreground mt-2 text-sm leading-relaxed">图注：{result.caption}</p>
          </div>

          <div className="border-border rounded-xl border p-4">
            <div className="text-muted-foreground mb-2 flex items-center gap-2 text-xs">
              <Table2 className="size-3.5" /> 解析后的表格（前 10 行，共 {result.table.rows.length} 行）
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead className="bg-muted/50 text-left">
                  <tr>
                    {result.table.headers.map((h) => (
                      <th key={h} className="px-2 py-1 font-medium">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {result.table.rows.slice(0, 10).map((row, i) => (
                    <tr key={i} className="border-border border-t">
                      {row.map((cell, j) => (
                        <td key={j} className="px-2 py-1">
                          {cell}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {(['mermaid', 'tikz'] as const).map((key) => (
            <div key={key} className="border-border rounded-xl border p-4">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-muted-foreground text-xs">
                  {key === 'mermaid' ? 'Mermaid（xychart-beta）' : 'TikZ / pgfplots'}
                </span>
                <Button size="sm" variant="outline" onClick={() => copy(key, result[key])}>
                  <Copy />
                  {copied === key ? '已复制' : '复制代码'}
                </Button>
              </div>
              <pre className="bg-muted/40 max-h-64 overflow-auto rounded-lg p-3 text-xs whitespace-pre-wrap">
                {result[key]}
              </pre>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}
