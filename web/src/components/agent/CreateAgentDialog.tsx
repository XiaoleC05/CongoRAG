import { useState } from 'react'
import { Link } from 'react-router'

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
import { ErrorText } from '@/components/ErrorText'
import { useCreateAgent, useToolCatalog } from '@/hooks/useAgents'
import { useErrorToast } from '@/hooks/useErrorToast'
import { useProviders } from '@/hooks/useProviders'
import { latestChatModel } from '@/lib/activeModel'
import { errorPresentation } from '@/lib/errors'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * 只把"toast 说不清"的错误交给下面的内联 ErrorText。
 *
 * 能重试的那类（internal_error / upstream_llm_error）已经由 useErrorToast
 * 弹过了，这里再渲染一份就是同一个错误两个 role="alert"。
 * 要用户改东西的（名字不合法之类）反过来：必须贴在字段旁边，弹 toast
 * 一闪而过用户根本来不及读。两份判据方向相反，同一条错误只出现一次。
 */
const inlineOnly = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * 创建 Agent 的表单弹窗。
 *
 * 【工具勾选列表来自 GET /api/v1/tools,不是硬编码】migrations/0005
 * 的种子数据是这份清单唯一的来源——表单不该自己知道"现在有哪几个
 * 工具",那样加一个新工具就要同时改后端种子数据和前端这个文件。
 */
export function CreateAgentDialog({ open, onOpenChange }: Props) {
  const { data: tools } = useToolCatalog()
  const { data: providers } = useProviders()
  const create = useCreateAgent()
  const showError = useErrorToast()

  // 【这个弹窗就必须回答「当前模型支不支持工具」】它是产生"带工具的 Agent"
  // 这个决定的地方，所以提示放在这里最有用——放到别处等于让用户在两次
  // 导航之外才知道自己配错了。
  //
  // 【useProviders 不会多打一次请求】路由的 RequireProvider 在渲染列表页
  // 之前就已经拉过一次，缓存是热的（顶多多一次后台核对，见 useProviders
  // 的 onSuccess 注释）。
  const activeModel = latestChatModel(providers ?? [])
  // 没有 chat 模型时不禁用——那时用户连 provider 都没配好，RequireProvider
  // 会先把他带去引导页，这里再报一次"模型不支持工具"是误报。
  const toolCallingUnsupported = activeModel !== null && !activeModel.capabilities.toolCalling

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [instruction, setInstruction] = useState('')
  const [selectedTools, setSelectedTools] = useState<string[]>([])

  const reset = () => {
    setName('')
    setDescription('')
    setInstruction('')
    setSelectedTools([])
    create.reset()
  }

  const toggleTool = (toolName: string, checked: boolean) => {
    setSelectedTools((prev) =>
      checked ? [...prev, toolName] : prev.filter((n) => n !== toolName),
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
            create.mutate(
              { name, description, instruction, toolNames: selectedTools },
              {
                onSuccess: () => {
                  reset()
                  onOpenChange(false)
                },
                // 失败时弹窗不关，输入都还在——能重试的错误走 toast 说一声，
                // 需要改内容的留在下面内联显示。
                onError: showError,
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>创建 Agent</DialogTitle>
            <DialogDescription>
              给它一个名字、系统提示词，选它能用哪些工具。
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="agent-name">名字</Label>
              <Input
                id="agent-name"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="计算助手"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="agent-description">描述（可选）</Label>
              <Input
                id="agent-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="帮助用户做算术运算"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="agent-instruction">系统提示词（可选）</Label>
              <textarea
                id="agent-instruction"
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
                placeholder="你是一个帮助用户做算术计算的助手，遇到需要计算的问题就调用 calculator 工具。"
                rows={3}
                className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px]"
              />
            </div>

            <div className="space-y-1.5">
              <Label>可用工具</Label>

              {toolCallingUnsupported && (
                <Alert variant="destructive">
                  <AlertTitle>当前聊天模型不支持工具调用</AlertTitle>
                  <AlertDescription>
                    <p>
                      生效的模型是 <span className="font-medium">{activeModel.modelId}</span>，
                      它没有声明「支持工具调用」。勾选工具后创建会被拒绝（服务端返回
                      400 并点名这个模型）。
                    </p>
                    <p className="mt-1.5">
                      要么不勾工具直接创建一个纯对话 Agent，要么到{' '}
                      <Link to="/onboarding" className="underline">
                        引导页
                      </Link>{' '}
                      重新保存配置并勾上「支持工具调用」。注意重新保存会新增一条配置，
                      生效的是最新的那一条。
                    </p>
                  </AlertDescription>
                </Alert>
              )}

              <div className="space-y-2">
                {tools?.map((tool) => (
                  <label key={tool.name} className="flex items-start gap-2 text-sm">
                    <Checkbox
                      checked={selectedTools.includes(tool.name)}
                      // 【禁用而不是隐藏】隐藏会让用户以为"这个 Agent 没有工具
                      // 可用"，禁用加上上面的说明才说得清"工具存在，但当前模型
                      // 用不了"。
                      //
                      // 【不加"必须至少选一个工具"这类提交条件】勾选框一禁用，
                      // selectedTools 恒为空，那种条件恒为 false，等于永远放行
                      // ——它自己就把自己抵消了。零工具 Agent 是合法配置，
                      // 正是这里的逃生口。
                      disabled={toolCallingUnsupported}
                      onCheckedChange={(checked) => toggleTool(tool.name, checked === true)}
                    />
                    <span>
                      <span className="font-medium">{tool.name}</span>
                      <span className="text-muted-foreground ml-1.5">{tool.description}</span>
                    </span>
                  </label>
                ))}
              </div>
            </div>

            <ErrorText error={inlineOnly(create.error)} />
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              取消
            </Button>
            <Button type="submit" disabled={create.isPending || name.trim() === ''}>
              {create.isPending ? '创建中…' : '创建'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
