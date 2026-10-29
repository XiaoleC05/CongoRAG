import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { BookOpen, Network } from 'lucide-react'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { useCitationGraph, useCitationSurvey } from '@/hooks/useDocuments'

/**
 * 局域引用图谱（参赛版 M5）。
 *
 * 后端把"每篇文档的参考文献命中库内标题"聚合成有向图并算 PageRank；
 * 这一块只做两件事：用 SVG 把图摆出来，把 PageRank 最高的几篇列成
 * "基石论文"表。不引第三方图库——几十个节点的环形布局用几十行 SVG
 * 就够，少一个依赖也少一份打包体积。
 */
const W = 720
const H = 440
const CX = W / 2
const CY = H / 2
const R = 150

function shortName(filename: string): string {
  return filename.replace(/\.pdf$/i, '')
}

export function KnowledgeCitationGraphPanel({ kbId }: { kbId: string }) {
  const navigate = useNavigate()
  const graph = useCitationGraph(kbId)
  const survey = useCitationSurvey(kbId)
  const [hovered, setHovered] = useState<string | null>(null)

  const result = graph.data
  const layout = useMemo(() => {
    if (!result || result.nodes.length === 0) return []
    const n = result.nodes.length
    return result.nodes.map((node, i) => {
      const angle = (2 * Math.PI * i) / n - Math.PI / 2
      const radius = 9 + Math.min(node.pageRank * 320, 16)
      return {
        node,
        x: CX + R * Math.cos(angle),
        y: CY + R * Math.sin(angle),
        r: radius,
      }
    })
  }, [result])

  const pos = useMemo(() => {
    const m = new Map<string, { x: number; y: number }>()
    for (const p of layout) m.set(p.node.documentId, { x: p.x, y: p.y })
    return m
  }, [layout])

  const top = result?.nodes.slice(0, 5) ?? []

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => graph.mutate()} disabled={graph.isPending}>
          <Network />
          {graph.isPending ? '计算中…' : '生成引用图谱'}
        </Button>
        <span className="text-muted-foreground text-xs">
          解析每篇文档的参考文献 → 匹配库内标题 → PageRank 挖基石论文
        </span>
      </div>

      {graph.error ? (
        <Alert variant="destructive">
          <AlertTitle>图谱生成失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={graph.error} />
          </AlertDescription>
        </Alert>
      ) : null}

      {result ? (
        result.nodes.length === 0 ? (
          <p className="text-muted-foreground text-sm">知识库里还没有 ready 文档，先上传几篇论文。</p>
        ) : (
          <div className="space-y-4">
            <div className="border-border overflow-x-auto rounded-xl border p-2">
              <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full min-w-[560px]" role="img" aria-label="引用关系图">
                <defs>
                  <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
                    <path d="M 0 0 L 10 5 L 0 10 z" className="fill-muted-foreground" />
                  </marker>
                </defs>
                {result.edges.map((e, i) => {
                  const a = pos.get(e.from)
                  const b = pos.get(e.to)
                  if (!a || !b) return null
                  return (
                    <line
                      key={i}
                      x1={a.x}
                      y1={a.y}
                      x2={b.x}
                      y2={b.y}
                      className="stroke-muted-foreground/40"
                      strokeWidth={1 + Math.min(e.weight, 3) * 0.6}
                      markerEnd="url(#arrow)"
                    />
                  )
                })}
                {layout.map(({ node, x, y, r }) => (
                  <g
                    key={node.documentId}
                    className="cursor-pointer"
                    onMouseEnter={() => setHovered(node.documentId)}
                    onMouseLeave={() => setHovered(null)}
                    onClick={() => navigate(`/documents/${node.documentId}/reader`)}
                  >
                    <circle
                      cx={x}
                      cy={y}
                      r={r}
                      className={node.cornerstone ? 'fill-amber-400' : 'fill-sky-500'}
                      fillOpacity={hovered === node.documentId ? 1 : 0.85}
                      stroke={hovered === node.documentId ? '#111827' : 'white'}
                      strokeWidth={2}
                    >
                      <title>
                        {node.filename}｜被引 {node.citationCount}｜PageRank {node.pageRank.toFixed(4)}
                      </title>
                    </circle>
                    <text x={x} y={y + r + 12} textAnchor="middle" className="fill-foreground text-[10px]">
                      {shortName(node.filename).slice(0, 16)}
                    </text>
                  </g>
                ))}
              </svg>
              <div className="text-muted-foreground mt-1 flex flex-wrap gap-4 px-2 text-xs">
                <span className="flex items-center gap-1">
                  <span className="inline-block size-3 rounded-full bg-amber-400" /> 基石论文
                </span>
                <span className="flex items-center gap-1">
                  <span className="inline-block size-3 rounded-full bg-sky-500" /> 普通论文
                </span>
                <span>箭头：引用方向（A → B 表示 A 引用了 B）</span>
              </div>
            </div>

            <div className="space-y-2">
              <div className="text-sm font-medium">基石论文（按 PageRank）</div>
              {top.length === 0 ? (
                <p className="text-muted-foreground text-sm">没有形成库内引用关系（参考文献没有命中库内标题）。</p>
              ) : (
                <div className="border-border overflow-hidden rounded-xl border">
                  <table className="w-full text-sm">
                    <thead className="bg-muted/50 text-muted-foreground text-left">
                      <tr>
                        <th className="px-3 py-2 font-medium">#</th>
                        <th className="px-3 py-2 font-medium">文档</th>
                        <th className="px-3 py-2 font-medium">被引</th>
                        <th className="px-3 py-2 font-medium">PageRank</th>
                        <th className="px-3 py-2 font-medium">标记</th>
                      </tr>
                    </thead>
                    <tbody>
                      {top.map((n, i) => (
                        <tr key={n.documentId} className="border-border border-t">
                          <td className="text-muted-foreground px-3 py-2">{i + 1}</td>
                          <td className="max-w-[260px] truncate px-3 py-2">{n.filename}</td>
                          <td className="px-3 py-2">{n.citationCount}</td>
                          <td className="px-3 py-2">{n.pageRank.toFixed(4)}</td>
                          <td className="px-3 py-2">
                            {n.cornerstone ? (
                              <span className="rounded bg-amber-100 px-1.5 py-0.5 text-xs text-amber-800">
                                基石
                              </span>
                            ) : (
                              <span className="text-muted-foreground text-xs">—</span>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>

            <div className="border-border rounded-xl border p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="text-muted-foreground flex items-center gap-2 text-xs">
                  <BookOpen className="size-3.5" /> 领域综述 / Future Work
                </div>
                <Button size="sm" variant="outline" onClick={() => survey.mutate(undefined)} disabled={survey.isPending}>
                  {survey.isPending ? '生成中…' : '基于图谱生成综述'}
                </Button>
              </div>
              {survey.error ? (
                <p className="mt-2 text-xs text-red-600">
                  <ErrorText error={survey.error} />
                </p>
              ) : null}
              {survey.data ? (
                <div className="mt-3 text-sm leading-relaxed [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm [&_li]:ml-4 [&_li]:list-disc [&_p]:mb-2">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{survey.data.markdown}</ReactMarkdown>
                </div>
              ) : (
                <p className="text-muted-foreground mt-2 text-xs">
                  基于库内论文片段与引用关系生成 400-600 字综述 + 3 条 Future Work，只使用给定材料。
                </p>
              )}
            </div>
          </div>
        )
      ) : null}
    </div>
  )
}
