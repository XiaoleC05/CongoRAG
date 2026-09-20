import { ArrowLeft, FileText, MessageSquarePlus, Trash2, Upload } from 'lucide-react'
import { useRef } from 'react'
import { Link, useNavigate, useParams } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { DocumentStatusBadge } from '@/components/knowledge/DocumentStatusBadge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useCreateConversation } from '@/hooks/useConversations'
import { useDocumentMutations, useDocuments } from '@/hooks/useDocuments'
import { useKnowledgeBases } from '@/hooks/useKnowledgeBases'
import { formatByteSize, formatDateTime } from '@/lib/format'

/**
 * 知识库详情页——文件列表 + 上传按钮。
 *
 * 【方案参照：Dify 文档表格】文件名 + 状态 Badge + 上传按钮，
 * 前端引用方案 §2.4 点名的形态。
 *
 * 【三态 + 空态，一个不能少】isPending/error 是所有页面的硬规矩
 *（web/README.md），空态（0 个文档）在这个页面尤其重要——新建的知识库
 * 打开详情页第一眼看到的就是空态，得清楚地引导去点上传。
 */
export default function KnowledgeBaseDetailPage() {
  const { id } = useParams<{ id: string }>()
  const kbId = id ?? ''

  // 知识库本身也可能还没加载完（比如直接用链接打开这个 URL），
  // 用它的名字做面包屑；找不到时退化成"知识库"这个通用文案，
  // 不阻塞文档列表本身的加载——两者是独立的数据源。
  const { data: kbs } = useKnowledgeBases()
  const kbName = kbs?.find((kb) => kb.id === kbId)?.name

  const { data: docs, isPending, error } = useDocuments(kbId)
  const { upload, remove } = useDocumentMutations(kbId)
  const createConversation = useCreateConversation()
  const navigate = useNavigate()

  const fileInputRef = useRef<HTMLInputElement>(null)

  // 【为什么"开始对话"在这里,而不是一个独立的"新建会话"页面】
  // 这一轮的会话天生要关联一个知识库才谈得上 RAG——见
  // internal/conversation/model.go 的注释:方案没有定义这个关联,
  // 是实现时补上的决策。知识库详情页因此是唯一自然的入口:
  // 用户已经在看"这个知识库有哪些文档",顺理成章地问"关于这些文档"。
  const handleStartConversation = () => {
    createConversation.mutate(
      { title: kbName ?? '新对话', knowledgeBaseId: kbId },
      { onSuccess: (conv) => navigate(`/conversations/${conv.id}`) },
    )
  }

  const handleFileSelected = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    // 选完立刻清空 input 的 value：不清的话，连续两次选同一个文件时
    // onChange 不会再触发（浏览器认为"值没变"），第二次上传就悄悄失效。
    e.target.value = ''
    if (file) {
      upload.mutate(file)
    }
  }

  return (
    <div className="mx-auto max-w-4xl p-6">
      <div className="mb-4">
        <Button variant="ghost" size="sm" asChild className="-ml-2">
          <Link to="/knowledge-bases">
            <ArrowLeft />
            返回知识库列表
          </Link>
        </Button>
      </div>

      <header className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold">{kbName ?? '知识库'}</h1>
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={handleStartConversation}
            disabled={createConversation.isPending}
          >
            <MessageSquarePlus />
            {createConversation.isPending ? '创建中…' : '开始对话'}
          </Button>
          <input
            ref={fileInputRef}
            type="file"
            className="hidden"
            accept=".md,.txt,.markdown"
            onChange={handleFileSelected}
          />
          <Button onClick={() => fileInputRef.current?.click()} disabled={upload.isPending}>
            <Upload />
            {upload.isPending ? '上传中…' : '上传文档'}
          </Button>
        </div>
      </header>

      <ErrorText error={upload.error} className="mb-4" />
      <ErrorText error={createConversation.error} className="mb-4" />

      {isPending ? (
        <SkeletonList />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : docs.length === 0 ? (
        <EmptyState onUpload={() => fileInputRef.current?.click()} />
      ) : (
        <div className="border-border overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-muted-foreground text-left">
              <tr>
                <th className="px-4 py-2 font-medium">文件名</th>
                <th className="px-4 py-2 font-medium">状态</th>
                <th className="px-4 py-2 font-medium">大小</th>
                <th className="px-4 py-2 font-medium">上传时间</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {docs.map((doc) => (
                <tr key={doc.id} className="border-border border-t">
                  <td className="flex items-center gap-2 px-4 py-2">
                    <FileText className="text-muted-foreground size-4 shrink-0" />
                    <span className="truncate" title={doc.filename}>
                      {doc.filename}
                    </span>
                  </td>
                  <td className="px-4 py-2">
                    <DocumentStatusBadge status={doc.status} />
                  </td>
                  <td className="text-muted-foreground px-4 py-2">
                    {formatByteSize(doc.byteSize)}
                  </td>
                  <td className="text-muted-foreground px-4 py-2">
                    {formatDateTime(doc.createdAt)}
                  </td>
                  <td className="px-4 py-2 text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`删除 ${doc.filename}`}
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(doc.id)}
                    >
                      <Trash2 className="text-muted-foreground" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <ErrorText error={remove.error} className="mt-4" />
    </div>
  )
}

function SkeletonList() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 4 }, (_, i) => (
        <Skeleton key={i} className="h-12 rounded-lg" />
      ))}
    </div>
  )
}

function EmptyState({ onUpload }: { onUpload: () => void }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <FileText className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">还没有文档</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        上传 Markdown 或纯文本文件，处理完就能检索问答。
      </p>
      <Button onClick={onUpload}>
        <Upload />
        上传第一个文档
      </Button>
    </div>
  )
}
