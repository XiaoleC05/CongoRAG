import { useState } from 'react'
import { useNavigate } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCreateProvider } from '@/hooks/useProviders'

/**
 * 引导页：首次打开填 Base URL + Key + 模型信息,"保存并开始"探测 embedding
 * 维度、建向量索引,然后进主界面。
 *
 * 【交互范式参照 Open WebUI】开发文档/前端引用方案 §2.1：一步式向导表单
 * (不是分步 wizard——本项目字段不多,不需要拆多步)。
 *
 * 【表单状态管理比知识库列表复杂的地方】NameDialog 只有一个字段,
 * 这里有嵌套的 chatModel 对象 + 4 个能力勾选 + 一个探测中的等待态。
 * 没有引入表单库（react-hook-form 之类）——字段数量还在"手写 useState
 * 更直接"的范围内,引入表单库要多学一层抽象,收益不成比例。
 */
export default function OnboardingPage() {
  const navigate = useNavigate()
  const createProvider = useCreateProvider()

  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [chatModelId, setChatModelId] = useState('')
  const [contextWindow, setContextWindow] = useState('')
  const [maxOutputTokens, setMaxOutputTokens] = useState('')
  const [tokenizerType, setTokenizerType] = useState('cl100k_base')
  const [embeddingModelId, setEmbeddingModelId] = useState('')

  const [streaming, setStreaming] = useState(true)
  const [toolCalling, setToolCalling] = useState(false)
  const [reasoning, setReasoning] = useState(false)

  // 客户端只做"必填"这一层最基本的校验——真正的强制在后端
  // (探测请求本身就是最强的校验：Key 错、模型不存在都会在那一步暴露)。
  const clientError = (() => {
    if (!baseUrl.trim()) return 'Base URL 不能为空'
    if (!apiKey.trim()) return 'API Key 不能为空'
    if (!chatModelId.trim()) return '聊天模型的名字不能为空'
    if (!Number(contextWindow) || Number(contextWindow) <= 0) return '上下文窗口必须是正整数'
    if (!Number(maxOutputTokens) || Number(maxOutputTokens) <= 0) return '最大输出 token 必须是正整数'
    if (!tokenizerType.trim()) return 'Tokenizer 类型不能为空'
    if (!embeddingModelId.trim()) return 'Embedding 模型的名字不能为空'
    return null
  })()

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    createProvider.mutate(
      {
        baseUrl: baseUrl.trim(),
        apiKey: apiKey.trim(),
        chatModel: {
          modelId: chatModelId.trim(),
          capabilities: { chat: true, streaming, toolCalling, reasoning },
          contextWindow: Number(contextWindow),
          maxOutputTokens: Number(maxOutputTokens),
          tokenizerType: tokenizerType.trim(),
        },
        embeddingModelId: embeddingModelId.trim(),
      },
      {
        onSuccess: () => navigate('/knowledge-bases', { replace: true }),
      },
    )
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-xl items-center p-6">
      <Card className="w-full">
        <CardHeader>
          <CardTitle>配置模型接入</CardTitle>
          <CardDescription>
            填入你的 OpenAI 兼容端点信息。保存时会用这些信息发一次真实请求，
            探测出 embedding 模型的输出维度，用来建向量索引——这一步需要几秒钟。
          </CardDescription>
        </CardHeader>

        <form onSubmit={handleSubmit}>
          <CardContent className="space-y-6">
            <section className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="baseUrl">Base URL</Label>
                <Input
                  id="baseUrl"
                  autoFocus
                  placeholder="https://api.example.com/v1"
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                  disabled={createProvider.isPending}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="apiKey">API Key</Label>
                <Input
                  id="apiKey"
                  type="password"
                  placeholder="sk-..."
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  disabled={createProvider.isPending}
                />
                <p className="text-muted-foreground text-xs">
                  落盘前用应用级主密钥加密，不会以明文存进数据库。
                </p>
              </div>
            </section>

            <section className="space-y-3 border-t pt-4">
              <h3 className="text-sm font-medium">聊天模型</h3>
              <div className="space-y-1.5">
                <Label htmlFor="chatModelId">模型名</Label>
                <Input
                  id="chatModelId"
                  placeholder="gpt-4o-mini"
                  value={chatModelId}
                  onChange={(e) => setChatModelId(e.target.value)}
                  disabled={createProvider.isPending}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <Label htmlFor="contextWindow">上下文窗口</Label>
                  <Input
                    id="contextWindow"
                    type="number"
                    placeholder="128000"
                    value={contextWindow}
                    onChange={(e) => setContextWindow(e.target.value)}
                    disabled={createProvider.isPending}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="maxOutputTokens">最大输出</Label>
                  <Input
                    id="maxOutputTokens"
                    type="number"
                    placeholder="4096"
                    value={maxOutputTokens}
                    onChange={(e) => setMaxOutputTokens(e.target.value)}
                    disabled={createProvider.isPending}
                  />
                </div>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="tokenizerType">Tokenizer</Label>
                <Input
                  id="tokenizerType"
                  placeholder="cl100k_base"
                  value={tokenizerType}
                  onChange={(e) => setTokenizerType(e.target.value)}
                  disabled={createProvider.isPending}
                />
                <p className="text-muted-foreground text-xs">
                  以上四项 OpenAI 兼容 API 不保证能自动查询，需要手填。
                </p>
              </div>
              <div className="flex flex-wrap gap-4 pt-1">
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={streaming}
                    onCheckedChange={(v) => setStreaming(v === true)}
                    disabled={createProvider.isPending}
                  />
                  支持流式输出
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={toolCalling}
                    onCheckedChange={(v) => setToolCalling(v === true)}
                    disabled={createProvider.isPending}
                  />
                  支持工具调用
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={reasoning}
                    onCheckedChange={(v) => setReasoning(v === true)}
                    disabled={createProvider.isPending}
                  />
                  推理模型
                </label>
              </div>
            </section>

            <section className="space-y-3 border-t pt-4">
              <h3 className="text-sm font-medium">Embedding 模型</h3>
              <div className="space-y-1.5">
                <Label htmlFor="embeddingModelId">模型名</Label>
                <Input
                  id="embeddingModelId"
                  placeholder="text-embedding-3-small"
                  value={embeddingModelId}
                  onChange={(e) => setEmbeddingModelId(e.target.value)}
                  disabled={createProvider.isPending}
                />
                <p className="text-muted-foreground text-xs">
                  只要模型名——维度由保存时的探测请求量出来，不用填。
                </p>
              </div>
            </section>

            {createProvider.error && (
              <Alert variant="destructive">
                <AlertTitle>保存失败</AlertTitle>
                <AlertDescription>
                  <ErrorText error={createProvider.error} />
                </AlertDescription>
              </Alert>
            )}
          </CardContent>

          <div className="flex justify-end gap-2 px-6 pb-6">
            <Button
              type="submit"
              disabled={createProvider.isPending || !!clientError}
            >
              {createProvider.isPending ? '探测中…' : '保存并开始'}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  )
}
