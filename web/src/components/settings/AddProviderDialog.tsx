import { useState } from 'react'
import { toast } from 'sonner'

import { ErrorText } from '@/components/ErrorText'
import { EmbeddingResetDialog } from '@/components/settings/EmbeddingResetDialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCreateProvider } from '@/hooks/useProviders'
import { useErrorToast } from '@/hooks/useErrorToast'
import { errorMessage, errorPresentation, errorType } from '@/lib/errors'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 只把"toast 说不清"的错误交给下面的内联 ErrorText（和 CreateAgentDialog
 * 同一条判据，见 lib/errors.ts 的 errorPresentation）。
 */
const inlineOnly = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * 新增一条模型接入（issue #83）。
 *
 * 【为什么叫"接入"而不是"模型"】契约里 `POST /api/v1/providers` 一次要传
 * 一整个 provider：baseUrl + apiKey + 一个 chat 模型 + 一个 embedding 模型
 * 名。没有"往已有 provider 上挂一个模型"这种端点（见文件末尾的说明），
 * 所以这个表单的粒度就是"一条接入"。
 *
 * 【字段与校验照抄引导页】两端共用同一批端点、同一批字段（issue #83 的
 * 说明里就是这么写的：引导页是"首次配置"，这里是"日常管理"）。差别只在
 * 这里不再负责建索引——探测维度和建向量列是服务端在同一个请求里做的，
 * 前端不需要知道。
 */
export function AddProviderDialog({ open, onOpenChange }: Props) {
  const createProvider = useCreateProvider()
  const showError = useErrorToast()

  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [chatModelId, setChatModelId] = useState('')
  const [contextWindow, setContextWindow] = useState('')
  const [maxOutputTokens, setMaxOutputTokens] = useState('')
  const [tokenizerType, setTokenizerType] = useState('cl100k_base')
  const [embeddingModelId, setEmbeddingModelId] = useState('')

  const [streaming, setStreaming] = useState(true)
  // 默认勾选的理由与引导页一致（issue #38）：这一位真的参与门控，
  // 默认不勾等于默认禁掉所有带工具的 Agent。
  const [toolCalling, setToolCalling] = useState(true)
  const [reasoning, setReasoning] = useState(false)

  // 客户端只做"必填"这一层最基本的校验——真正的强制在后端（探测请求本身
  // 就是最强的校验：Key 错、模型不存在都会在那一步暴露，见 §6）。
  const clientError = (() => {
    if (!baseUrl.trim()) return 'Base URL 不能为空'
    if (!apiKey.trim()) return 'API Key 不能为空'
    if (!chatModelId.trim()) return '聊天模型的名字不能为空'
    if (!Number(contextWindow) || Number(contextWindow) <= 0) return '上下文窗口必须是正整数'
    if (!Number(maxOutputTokens) || Number(maxOutputTokens) <= 0)
      return '最大输出 token 必须是正整数'
    if (!tokenizerType.trim()) return 'Tokenizer 类型不能为空'
    if (!embeddingModelId.trim()) return 'Embedding 模型的名字不能为空'
    return null
  })()

  // 【换 embedding 模型是一条确认流程，不是一条报错路径（issue #39）】
  const [resetPromptOpen, setResetPromptOpen] = useState(false)

  const reset = () => {
    setBaseUrl('')
    setApiKey('')
    setChatModelId('')
    setContextWindow('')
    setMaxOutputTokens('')
    setTokenizerType('cl100k_base')
    setEmbeddingModelId('')
    setStreaming(true)
    setToolCalling(true)
    setReasoning(false)
    setResetPromptOpen(false)
    createProvider.reset()
  }

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
        onSuccess: (provider) => {
          // 【成功提示不能只是"报喜"】写入结果由 useCreateProvider 里的
          // 写穿 + invalidateQueries 落到列表上（§3 / §16）——弹窗关掉之后
          // 用户立刻能在下面看到新的一条。toast 只是补一句"刚才那一步成了"。
          //
          // requeuedDocuments 只在真的执行了重建时才是数字（契约里写明了），
          // 所以这句话不能无脑拼上去。
          const requeued = provider.requeuedDocuments
          toast.success(
            requeued
              ? `已保存，正在重建 ${requeued} 份文档的向量`
              : '已保存模型接入',
          )
          reset()
          onOpenChange(false)
        },
        onError: (err) => {
          if (errorType(err) === 'embedding_change_requires_reindex') {
            setResetPromptOpen(true)
            return
          }
          showError(err)
        },
      },
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) reset()
        onOpenChange(next)
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit(false)
          }}
        >
          <DialogHeader>
            <DialogTitle>新增模型接入</DialogTitle>
            <DialogDescription>
              保存时会用这些信息发一次真实请求，探测 embedding 模型的输出维度。
              这一步需要几秒钟。
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="settings-baseUrl">Base URL</Label>
              <Input
                id="settings-baseUrl"
                autoFocus
                placeholder="https://api.example.com/v1"
                value={baseUrl}
                onChange={(e) => setBaseUrl(e.target.value)}
                disabled={createProvider.isPending}
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="settings-apiKey">API Key</Label>
              <Input
                id="settings-apiKey"
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

            <fieldset className="space-y-3 border-t pt-4">
              <legend className="text-sm font-medium">聊天模型</legend>
              <div className="space-y-1.5">
                <Label htmlFor="settings-chatModelId">模型名</Label>
                <Input
                  id="settings-chatModelId"
                  placeholder="gpt-4o-mini"
                  value={chatModelId}
                  onChange={(e) => setChatModelId(e.target.value)}
                  disabled={createProvider.isPending}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <Label htmlFor="settings-contextWindow">上下文窗口</Label>
                  <Input
                    id="settings-contextWindow"
                    type="number"
                    placeholder="128000"
                    value={contextWindow}
                    onChange={(e) => setContextWindow(e.target.value)}
                    disabled={createProvider.isPending}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="settings-maxOutputTokens">最大输出</Label>
                  <Input
                    id="settings-maxOutputTokens"
                    type="number"
                    placeholder="4096"
                    value={maxOutputTokens}
                    onChange={(e) => setMaxOutputTokens(e.target.value)}
                    disabled={createProvider.isPending}
                  />
                </div>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="settings-tokenizerType">Tokenizer</Label>
                <Input
                  id="settings-tokenizerType"
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
            </fieldset>

            <fieldset className="space-y-3 border-t pt-4">
              <legend className="text-sm font-medium">Embedding 模型</legend>
              <div className="space-y-1.5">
                <Label htmlFor="settings-embeddingModelId">模型名</Label>
                <Input
                  id="settings-embeddingModelId"
                  placeholder="text-embedding-3-small"
                  value={embeddingModelId}
                  onChange={(e) => setEmbeddingModelId(e.target.value)}
                  disabled={createProvider.isPending}
                />
                <p className="text-muted-foreground text-xs">
                  只要模型名——维度由保存时的探测请求量出来，不用填。
                  这一项填得和当前生效的不一样时，会先要求你确认清空重建。
                </p>
              </div>
            </fieldset>

            {/* 【内联只显示"toast 说不清"的那些】判据在 inlineOnly 里。
                embedding_change_requires_reindex 属于"需要确认"而不是
                "失败"，它由上面的确认框解释——这里再渲染一份就是同一个
                信息说两遍（它本来就落在 toast 那一类，inlineOnly 会滤掉）。 */}
            {inlineOnly(createProvider.error) && (
              <Alert variant="destructive">
                <AlertTitle>保存失败</AlertTitle>
                <AlertDescription>
                  <ErrorText error={createProvider.error} />
                </AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={createProvider.isPending}
            >
              取消
            </Button>
            <Button type="submit" disabled={createProvider.isPending || !!clientError}>
              {createProvider.isPending ? '探测中…' : '保存'}
            </Button>
          </DialogFooter>
        </form>

        <EmbeddingResetDialog
          open={resetPromptOpen}
          onOpenChange={setResetPromptOpen}
          message={createProvider.error ? errorMessage(createProvider.error) : ''}
          pending={createProvider.isPending}
          onConfirm={() => {
            setResetPromptOpen(false)
            submit(true)
          }}
        />
      </DialogContent>
    </Dialog>
  )
}
