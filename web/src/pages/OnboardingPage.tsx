import { useState } from 'react'
import { useNavigate } from 'react-router'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCreateProvider } from '@/hooks/useProviders'
import { errorMessage, errorType } from '@/lib/errors'

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
  // 【默认勾选（issue #38）】这一位现在真的参与门控：模型没有声明它、
  // 而 Agent 要用工具时，创建和运行都会被拒。默认不勾等于默认禁掉所有
  // 带工具的 Agent——那是升级即坏。契约里的 default 也是 true，
  // 迁移 0007 还把升级前就存在的 chat 行回填了，三处是同一件事。
  const [toolCalling, setToolCalling] = useState(true)
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

  // 【换 embedding 模型是一条确认流程，不是一条报错路径（issue #39）】
  // 服务端发现库里的向量不属于这个模型时会返回 409，但那不是"你做错了什么"——
  // 用户改任何一个字段都过不去，唯一的出路是确认"清空并重建"。所以这里弹的是
  // 确认框，不是红字。
  const [resetPromptOpen, setResetPromptOpen] = useState(false)

  const submit = (allowEmbeddingReset: boolean) => {
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
        allowEmbeddingReset,
      },
      {
        onSuccess: () => navigate('/knowledge-bases', { replace: true }),
        onError: (err) => {
          if (errorType(err) === 'embedding_change_requires_reindex') {
            setResetPromptOpen(true)
          }
        },
      },
    )
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    submit(false)
  }

  // 这个 409 不该同时以红字显示——它已经被上面那个确认框解释过了，
  // 两边都出现等于同一个信息说两遍。
  const resetRequired = errorType(createProvider.error) === 'embedding_change_requires_reindex'

  return (
    <div className="mx-auto flex min-h-screen max-w-xl items-center p-6">
      <Card className="w-full">
        <CardHeader>
          {/* 【h1 为什么嵌在 CardTitle 里面（issue #95）】CardTitle 渲染的是一个
              div——它是排版用的容器，不是标题标签。这一页没有第二个标题，
              这张卡片的标题就是页面的主标题，所以真正的 <h1> 放在它里面。
              不写样式类：Tailwind 的 preflight 把 h1 的 font-size / font-weight
              重置成 inherit，字号字重直接从 CardTitle 继承下来，视觉上完全一样。
              反过来，用一个大字号的 div 去"看起来像标题"才是要避免的那种写法——
              读屏软件的大纲里会缺一层，用户按标题跳转时跳不到这一页。

              页内两个 section 标题（聊天模型 / Embedding 模型）是 h2：
              它们挂在 h1 下面，大纲是连续的，没有断层。 */}
          <CardTitle>
            <h1>配置模型接入</h1>
          </CardTitle>
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
              {/* h2 而不是 h3：上面那张卡片标题现在是 h1，这两个分组标题是它
                  的直接下级。层级用标签表达，字号（text-sm）是它自己的排版决定，
                  两者不必一致——"看起来比 h1 小"跟"在大纲里挂在哪一层"是两件事。 */}
              <h2 className="text-sm font-medium">聊天模型</h2>
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
              <h2 className="text-sm font-medium">Embedding 模型</h2>
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

            {createProvider.error && !resetRequired && (
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

        <AlertDialog open={resetPromptOpen} onOpenChange={setResetPromptOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>要换 embedding 模型吗？</AlertDialogTitle>
              <AlertDialogDescription>
                {createProvider.error ? errorMessage(createProvider.error) : ''}
              </AlertDialogDescription>
            </AlertDialogHeader>

            <p className="text-muted-foreground text-sm">
              确认之后服务端会在同一个事务里清空这些向量、把列改成新模型的维度，
              并把全部文档重新排队重建。重建是后台异步做的，期间检索会返回空结果；
              文档列表里能看到它们重新变成「处理中」，跑完就恢复。
            </p>

            <AlertDialogFooter>
              <AlertDialogCancel disabled={createProvider.isPending}>取消</AlertDialogCancel>
              <AlertDialogAction
                disabled={createProvider.isPending}
                onClick={(e) => {
                  // 不阻止的话弹窗会立刻关闭，而请求还在飞——用户看不到
                  // "正在探测"这个中间态。
                  e.preventDefault()
                  setResetPromptOpen(false)
                  submit(true)
                }}
              >
                清空并重建
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </Card>
    </div>
  )
}
