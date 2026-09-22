import { useState } from 'react'
import { toast } from 'sonner'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
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
import { useModelMutations } from '@/hooks/useProviders'
import { useErrorToast } from '@/hooks/useErrorToast'
import { errorPresentation } from '@/lib/errors'

type ModelSummary = Schemas['ModelSummary']

type Props = {
  /** null 表示没有待编辑的模型，弹窗关闭 */
  model: ModelSummary | null
  onOpenChange: (open: boolean) => void
}

/** 只把"toast 说不清"的错误交给下面的内联 ErrorText（判据见 lib/errors.ts）。 */
const inlineOnly = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * 编辑一个模型条目（issue #83 的"能新增 / 编辑 / 删除模型，能回看并修改
 * capabilities"）。
 *
 * 【能改什么、不能改什么，是契约定的，不是这个表单定的】
 *   - 能改：模型名（仅 chat）、capabilities、上下文窗口、最大输出、tokenizer。
 *   - 不能改：`kind` 与所属 provider（改它等于换了一个模型，该新建一条）；
 *     **embedding 模型的模型名**——向量列的维度是按那个名字探测出来并
 *     ALTER 到表上的（ADR-004），改名字会让"列里这些向量是谁算的"和配置
 *     对不上。要换 embedding 模型，走那条会清空重建的流程（设置页的
 *     "新增接入"里有）。
 *
 * 【为什么把不能改的那项渲染成 disabled，而不是不渲染】用户需要看到
 * "这个模型叫什么"才能确认自己没点错行。藏起来反而让他回头去对别的格子。
 *
 * 【初始值从 props 读，靠 key 重新挂载来刷新】§8：不要用 useEffect 监听
 * open 再 setState（多一次渲染，oxlint 的 react(set-state-in-effect) 也会报）。
 * 调用方每次都换 key，所以这里 useState 的初始值就是"这一次打开时的真实值"。
 */
export function EditModelDialog({ model, onOpenChange }: Props) {
  const { update } = useModelMutations()
  const showError = useErrorToast()

  const [modelId, setModelId] = useState(model?.modelId ?? '')
  const [contextWindow, setContextWindow] = useState(String(model?.contextWindow ?? ''))
  const [maxOutputTokens, setMaxOutputTokens] = useState(String(model?.maxOutputTokens ?? ''))
  const [tokenizerType, setTokenizerType] = useState(model?.tokenizerType ?? '')
  // capabilities 对 embedding 模型没有意义（契约里四个开关是给 chat 模型
  // 用的），但请求体里这一项是必填的——所以照原样带着，不改它。
  const [capabilities, setCapabilities] = useState(
    model?.capabilities ?? { chat: true, streaming: false, toolCalling: true, reasoning: false },
  )

  const isEmbedding = model?.kind === 'embedding'

  const clientError = (() => {
    // embedding 的名字不可改，这一格不参与校验——否则禁用着一个空值会
    // 让"保存"永远点不动，而且用户看不出为什么。
    if (!isEmbedding && !modelId.trim()) return '模型名不能为空'
    if (!Number(contextWindow) || Number(contextWindow) <= 0) return '上下文窗口必须是正整数'
    if (!Number(maxOutputTokens) || Number(maxOutputTokens) <= 0)
      return '最大输出 token 必须是正整数'
    if (!tokenizerType.trim()) return 'Tokenizer 不能为空'
    return null
  })()

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!model) return

    update.mutate(
      {
        id: model.id,
        body: {
          // 【整体替换，不是字段级合并】契约里这五个字段全部 required，
          // 语义是"用我给的这一份替换掉原来的"。哪些不给人改的（embedding
          // 的名字），就把当前值原样回传——注意不是空串：禁用输入框在表单里
          // 仍然带着 value，回传空串会被服务端当成"要把名字改成空的"。
          modelId: isEmbedding ? model.modelId : modelId.trim(),
          capabilities,
          contextWindow: Number(contextWindow),
          maxOutputTokens: Number(maxOutputTokens),
          tokenizerType: tokenizerType.trim(),
        },
      },
      {
        onSuccess: () => {
          toast.success('已保存')
          onOpenChange(false)
        },
        onError: showError,
      },
    )
  }

  return (
    <Dialog open={!!model} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>编辑模型</DialogTitle>
            <DialogDescription>
              改的是这条模型的声明。名字和维度这些"身份"信息受契约约束，
              有些格子改不了。
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="edit-model-name">模型名</Label>
              <Input
                id="edit-model-name"
                value={modelId}
                onChange={(e) => setModelId(e.target.value)}
                disabled={isEmbedding || update.isPending}
              />
              {isEmbedding && (
                <p className="text-muted-foreground text-xs">
                  embedding 模型的模型名不能改：向量列的维度是按这个名字探测出来
                  并写进表结构的，改名会让列里的向量和配置对不上。要换模型，
                  用「新增接入」走那条会清空重建的流程。
                </p>
              )}
            </div>

            {!isEmbedding && (
              <div className="space-y-1.5">
                <Label>能力声明</Label>
                <div className="flex flex-wrap gap-4">
                  {/* 只留三个可勾的：`chat` 这一位是服务端按 kind 定的
                      （契约注释写明），用户勾不勾都不影响事实。 */}
                  <label className="flex items-center gap-2 text-sm">
                    <Checkbox
                      checked={capabilities.streaming}
                      onCheckedChange={(v) =>
                        setCapabilities((c) => ({ ...c, streaming: v === true }))
                      }
                      disabled={update.isPending}
                    />
                    支持流式输出
                  </label>
                  <label className="flex items-center gap-2 text-sm">
                    <Checkbox
                      checked={capabilities.toolCalling}
                      onCheckedChange={(v) =>
                        setCapabilities((c) => ({ ...c, toolCalling: v === true }))
                      }
                      disabled={update.isPending}
                    />
                    支持工具调用
                  </label>
                  <label className="flex items-center gap-2 text-sm">
                    <Checkbox
                      checked={capabilities.reasoning}
                      onCheckedChange={(v) =>
                        setCapabilities((c) => ({ ...c, reasoning: v === true }))
                      }
                      disabled={update.isPending}
                    />
                    推理模型
                  </label>
                </div>
                {/* 【这一位为什么要提醒】它真的参与门控：模型没声明工具调用、
                    而 Agent 要用工具时，创建和运行都会被拒（issue #38）。 */}
                <p className="text-muted-foreground text-xs">
                  取消勾选「支持工具调用」会让依赖工具的 Agent 创建和运行被拒。
                </p>
              </div>
            )}

            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="edit-model-context">上下文窗口</Label>
                <Input
                  id="edit-model-context"
                  type="number"
                  value={contextWindow}
                  onChange={(e) => setContextWindow(e.target.value)}
                  disabled={update.isPending}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="edit-model-max-output">最大输出</Label>
                <Input
                  id="edit-model-max-output"
                  type="number"
                  value={maxOutputTokens}
                  onChange={(e) => setMaxOutputTokens(e.target.value)}
                  disabled={update.isPending}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="edit-model-tokenizer">Tokenizer</Label>
              <Input
                id="edit-model-tokenizer"
                value={tokenizerType}
                onChange={(e) => setTokenizerType(e.target.value)}
                disabled={update.isPending}
              />
              <p className="text-muted-foreground text-xs">
                tiktoken 的编码名，比如 o200k_base。取值必须是服务端已认识的类型。
              </p>
            </div>

            {inlineOnly(update.error) && (
              <Alert variant="destructive">
                <AlertTitle>保存失败</AlertTitle>
                <AlertDescription>
                  <ErrorText error={update.error} />
                </AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={update.isPending}
            >
              取消
            </Button>
            <Button type="submit" disabled={update.isPending || !!clientError}>
              {update.isPending ? '保存中…' : '保存'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
