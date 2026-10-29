import { useState } from 'react'
import { ArrowDownToLine, Code2, ExternalLink, Sparkles, ThumbsDown, ThumbsUp } from 'lucide-react'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  useArxivFeedback,
  useArxivIngest,
  useArxivRecommendations,
} from '@/hooks/useDocuments'

/**
 * arXiv 最新论文推荐（参赛版 M4）。
 *
 * 后端用库内标题关键词查 arXiv，再按相关性/新近度/开源代码重排并给出理由；
 * 这里只负责展示与三个动作：赞、踩、下载入库。赞/踩持久化后，down 的
 * 论文下次不再出现（服务端过滤）。
 */
export function KnowledgeArxivPanel({ kbId }: { kbId: string }) {
  const rec = useArxivRecommendations(kbId)
  const feedback = useArxivFeedback(kbId)
  const ingest = useArxivIngest(kbId)
  const [votes, setVotes] = useState<Record<string, 'up' | 'down'>>({})
  const [ingested, setIngested] = useState<Record<string, string>>({})
  const [ingestError, setIngestError] = useState<unknown>(null)
  const items = rec.data?.items ?? []

  const vote = (arxivId: string, v: 'up' | 'down') => {
    setVotes((prev) => ({ ...prev, [arxivId]: v }))
    feedback.mutate(
      { arxivId, vote: v },
      { onError: () => setVotes((prev) => ({ ...prev, [arxivId]: votes[arxivId] ?? 'up' })) },
    )
  }

  const handleIngest = (item: { arxivId: string; pdfUrl: string; title: string }) => {
    setIngestError(null)
    ingest.mutate(
      { arxivId: item.arxivId, pdfUrl: item.pdfUrl, title: item.title },
      {
        onSuccess: (doc) => setIngested((prev) => ({ ...prev, [item.arxivId]: doc.id })),
        onError: (e) => setIngestError(e),
      },
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => rec.mutate()} disabled={rec.isPending}>
          <Sparkles />
          {rec.isPending ? '检索中…' : '推荐 arXiv 最新论文'}
        </Button>
        <span className="text-muted-foreground text-xs">
          用库内标题关键词检索 arXiv，按相关性 / 新近度 / 是否带 GitHub 代码排序
        </span>
      </div>

      {rec.error ? (
        <Alert variant="destructive">
          <AlertTitle>推荐失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={rec.error} />
          </AlertDescription>
        </Alert>
      ) : null}
      {ingestError ? (
        <Alert variant="destructive">
          <AlertTitle>下载入库失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={ingestError} />
          </AlertDescription>
        </Alert>
      ) : null}

      {items.length > 0 ? (
        <ul className="space-y-3">
          {items.map((item) => {
            const currentVote = votes[item.arxivId] ?? (item.vote || undefined)
            const docId = ingested[item.arxivId]
            return (
              <li key={item.arxivId} className="border-border rounded-xl border p-4">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <a
                      href={item.url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-primary text-sm font-medium hover:underline"
                    >
                      {item.title} <ExternalLink className="inline size-3" />
                    </a>
                    <div className="text-muted-foreground mt-1 text-xs">
                      {item.authors.slice(0, 3).join(', ')}
                      {item.authors.length > 3 ? ' 等' : ''} · {item.published.slice(0, 10)} · arXiv:
                      {item.arxivId}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button
                      size="sm"
                      variant={currentVote === 'up' ? 'default' : 'outline'}
                      aria-label="赞"
                      onClick={() => vote(item.arxivId, 'up')}
                      disabled={feedback.isPending}
                    >
                      <ThumbsUp />
                    </Button>
                    <Button
                      size="sm"
                      variant={currentVote === 'down' ? 'default' : 'outline'}
                      aria-label="踩"
                      onClick={() => vote(item.arxivId, 'down')}
                      disabled={feedback.isPending}
                    >
                      <ThumbsDown />
                    </Button>
                  </div>
                </div>

                <p className="text-muted-foreground mt-2 line-clamp-3 text-sm leading-relaxed">
                  {item.abstract}
                </p>
                <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
                  <span className="bg-muted rounded px-2 py-0.5">{item.reason}</span>
                  {item.githubUrl ? (
                    <a
                      href={item.githubUrl}
                      target="_blank"
                      rel="noreferrer"
                      className="text-primary inline-flex items-center gap-1 hover:underline"
                    >
                      <Code2 className="size-3.5" /> GitHub
                    </a>
                  ) : null}
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      handleIngest({
                        arxivId: item.arxivId,
                        pdfUrl: item.pdfUrl,
                        title: item.title,
                      })
                    }
                    disabled={ingest.isPending}
                  >
                    <ArrowDownToLine />
                    {ingest.isPending ? '下载中…' : '下载并入知识库'}
                  </Button>
                  {docId ? <span className="text-xs text-emerald-600">已入库，处理中</span> : null}
                </div>
              </li>
            )
          })}
        </ul>
      ) : null}
    </div>
  )
}
