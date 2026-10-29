import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { PenLine, FileText } from 'lucide-react'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useWritingDraft } from '@/hooks/useDocuments'
import type { WritingKind } from '@/hooks/useDocuments'

const KINDS: { value: WritingKind; label: string; hint: string }[] = [
  { value: 'related_work', label: '相关工作综述', hint: '按方法脉络组织的一段综述' },
  { value: 'summary', label: '摘要式总结', hint: '200~300 字概括核心方法与结论' },
  { value: 'outline', label: '章节提纲', hint: 'Markdown 多级列表的写作骨架' },
]

/**
 * 写作 Copilot（参赛版 M6）。
 *
 * 【为什么它和检索调试摆在同一页】写作草稿的可信度完全取决于检索质量：
 * 用户先在检索调试里确认"召回对不对"，再来这里生成草稿，两条证据
 * 放在同一页才能互相印证。草稿的每条 [n] 都对应下面列出的引用，
 * 点页码可以直接跳阅读器核对原文。
 */
export function KnowledgeWritingPanel({ kbId }: { kbId: string }) {
  const [topic, setTopic] = useState('')
  const [kind, setKind] = useState<WritingKind>('related_work')
  const [submitError, setSubmitError] = useState<string | null>(null)
  const draft = useWritingDraft(kbId)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const t = topic.trim()
    if (t === '') {
      setSubmitError('先输入写作主题')
      return
    }
    setSubmitError(null)
    draft.mutate({ topic: t, kind })
  }

  const result = draft.data

  return (
    <div className="space-y-4">
      <form onSubmit={handleSubmit} className="flex flex-col gap-3 lg:flex-row lg:items-end">
        <div className="min-w-0 flex-1 space-y-1.5">
          <Label htmlFor="writing-topic">写作主题</Label>
          <Input
            id="writing-topic"
            value={topic}
            onChange={(e) => {
              setTopic(e.target.value)
              setSubmitError(null)
            }}
            placeholder="例如：Transformer 的高效注意力机制"
            aria-invalid={submitError ? true : undefined}
          />
          {submitError ? <p className="text-xs text-red-600">{submitError}</p> : null}
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="writing-kind">草稿类型</Label>
          <select
            id="writing-kind"
            value={kind}
            onChange={(e) => setKind(e.target.value as WritingKind)}
            className="border-input bg-background h-9 rounded-md border px-3 text-sm"
          >
            {KINDS.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </select>
        </div>
        <Button type="submit" disabled={draft.isPending}>
          <PenLine />
          {draft.isPending ? '生成中…' : '生成草稿'}
        </Button>
      </form>
      <p className="text-muted-foreground text-xs">
        {KINDS.find((k) => k.value === kind)?.hint} · 只依据当前知识库的检索结果写作，
        资料不足时会直接说明。
      </p>

      {draft.error ? (
        <Alert variant="destructive">
          <AlertTitle>生成失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={draft.error} />
          </AlertDescription>
        </Alert>
      ) : null}

      {result ? (
        <div className="space-y-4">
          <div className="border-border rounded-xl border p-4">
            <div className="text-muted-foreground mb-2 flex items-center gap-2 text-xs">
              <PenLine className="size-3.5" />
              草稿（Markdown，可复制到论文里继续改）
            </div>
            <div className="text-sm leading-relaxed [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm [&_li]:ml-4 [&_li]:list-disc [&_p]:mb-2">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{result.draft}</ReactMarkdown>
            </div>
          </div>

          <div className="space-y-2">
            <div className="text-muted-foreground flex items-center gap-2 text-xs">
              <FileText className="size-3.5" />
              依据（{result.citations.length} 条，草稿里的 [n] 对应这里的序号）
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
                </div>
                <p className="text-muted-foreground mt-1 line-clamp-3 whitespace-pre-wrap">{c.snippet}</p>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  )
}
