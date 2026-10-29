import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { FileText, GitCompareArrows } from 'lucide-react'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCompareDraft } from '@/hooks/useDocuments'

/**
 * 多篇对比问答（参赛版 A6）。
 *
 * 【为什么要单独一块 UI】聊天给出的是一段线性叙述，横向比较两篇论文的
 * 差异时要来回翻上下文。这里直接把"按维度组织的对照表 + 综合结论"摆出来，
 * 并把每条依据按文档列在下面，点页码就能回原文核对。
 */
export function KnowledgeComparePanel({ kbId }: { kbId: string }) {
  const [question, setQuestion] = useState('')
  const [submitError, setSubmitError] = useState<string | null>(null)
  const compare = useCompareDraft(kbId)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const q = question.trim()
    if (q === '') {
      setSubmitError('先输入要对比的问题')
      return
    }
    setSubmitError(null)
    compare.mutate({ question: q })
  }

  const result = compare.data
  return (
    <div className="space-y-4">
      <form onSubmit={handleSubmit} className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1 space-y-1.5">
          <Label htmlFor="compare-question">对比问题</Label>
          <Input
            id="compare-question"
            value={question}
            onChange={(e) => {
              setQuestion(e.target.value)
              setSubmitError(null)
            }}
            placeholder="例如：这些论文如何解决长距离依赖问题？"
            aria-invalid={submitError ? true : undefined}
          />
          {submitError ? <p className="text-xs text-red-600">{submitError}</p> : null}
        </div>
        <Button type="submit" disabled={compare.isPending}>
          <GitCompareArrows />
          {compare.isPending ? '对比中…' : '生成对比'}
        </Button>
      </form>
      <p className="text-muted-foreground text-xs">
        从当前知识库检索多条证据，按主题列成对照表，并保留每条依据的页码。
      </p>

      {compare.error ? (
        <Alert variant="destructive">
          <AlertTitle>对比失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={compare.error} />
          </AlertDescription>
        </Alert>
      ) : null}

      {result ? (
        <div className="space-y-4">
          <div className="border-border rounded-xl border p-4">
            <div className="text-muted-foreground mb-2 flex items-center gap-2 text-xs">
              <GitCompareArrows className="size-3.5" />
              对比结果（Markdown）
            </div>
            <div className="text-sm leading-relaxed [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm [&_li]:ml-4 [&_li]:list-disc [&_p]:mb-2 [&_table]:w-full [&_td]:border [&_td]:p-1.5 [&_th]:border [&_th]:bg-muted [&_th]:p-1.5">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{result.answer}</ReactMarkdown>
            </div>
          </div>

          <div className="space-y-2">
            <div className="text-muted-foreground flex items-center gap-2 text-xs">
              <FileText className="size-3.5" />
              证据（{result.citations.length} 条，[n] 对应这里）
            </div>
            {result.citations.map((c, i) => (
              <div key={c.chunkId} className="border-border rounded-lg border p-3 text-xs">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-primary font-medium">[{i + 1}]</span>
                  <span className="truncate font-medium">{c.filename}</span>
                  {c.page != null ? (
                    <a
                      className="text-primary hover:underline"
                      href={`/documents/${c.documentId}/reader?page=${c.page}`}
                      target="_blank"
                      rel="noreferrer"
                    >
                      第 {c.page} 页 ↗
                    </a>
                  ) : null}
                  {c.sectionPath && c.sectionPath.length > 0 ? (
                    <span className="text-muted-foreground truncate">
                      {c.sectionPath[c.sectionPath.length - 1]}
                    </span>
                  ) : null}
                </div>
                <p className="text-muted-foreground mt-1 line-clamp-2 whitespace-pre-wrap">{c.snippet}</p>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  )
}
