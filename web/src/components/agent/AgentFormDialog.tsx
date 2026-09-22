import { useState } from 'react'
import { Link } from 'react-router'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
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
import {
  useCreateAgent,
  useToolCatalog,
  useUpdateAgent,
  validateDescription,
  validateInstruction,
} from '@/hooks/useAgents'
import type { Agent } from '@/hooks/useAgents'
import { useErrorToast } from '@/hooks/useErrorToast'
import { useProviders } from '@/hooks/useProviders'
import { latestChatModel } from '@/lib/activeModel'
import { errorPresentation } from '@/lib/errors'
import { validateName } from '@/lib/validation'

type SideEffectLevel = Schemas['ToolCatalogEntry']['sideEffectLevel']

/**
 * 工具的副作用等级 → 中文。
 *
 * 【为什么这一列必须在勾选时看得见】它回答的是"勾上这个工具，Agent 会不
 * 会动我的数据"。三个取值来自后端（`internal/agent/model.go` 的
 * SideEffectLevel，契约里 ToolCatalogEntry.sideEffectLevel 是必填），
 * 而且真的参与运行期判定：崩溃恢复时 WRITE_NON_IDEMPOTENT 的步骤会被
 * 拒绝自动重放（`internal/agent/usecase.go` 的 gateToolReplay）。
 * 用户看不到这一位，就只能靠工具名猜。
 *
 * 【用 Record 而不是宽松索引】契约加一个新等级时这里会直接编译不过；
 * 宽松索引会静默漏一档，界面上那一行什么也不显示，也不报错。
 */
const SIDE_EFFECT_LABEL: Record<SideEffectLevel, string> = {
  READ_ONLY: '只读',
  WRITE_IDEMPOTENT: '写入（可重复）',
  WRITE_NON_IDEMPOTENT: '写入（不可重复）',
}

/** 徽章的配色。只读用 outline，会写数据的两档用 secondary——它们更该被看见。 */
const SIDE_EFFECT_VARIANT: Record<SideEffectLevel, 'outline' | 'secondary'> = {
  READ_ONLY: 'outline',
  WRITE_IDEMPOTENT: 'secondary',
  WRITE_NON_IDEMPOTENT: 'secondary',
}

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 传了就是编辑这一条；不传是新建。两种模式共用一份表单（issue #81）。 */
  agent?: Agent
}

/**
 * 只把"toast 说不清"的错误交给下面的内联 ErrorText。
 *
 * 能重试的那类（internal_error / upstream_llm_error）已经由 useErrorToast
 * 弹过了，这里再渲染一份就是同一个错误两个 role="alert"，读屏软件念两遍。
 * 要用户改东西的（名字不合法、工具没注册之类）反过来：必须贴在字段旁边，
 * 弹 toast 一闪而过用户根本来不及读。判据在 lib/errors.ts，不在这里自己判断。
 */
const inlineOnly = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * Agent 配置表单（issue #81）——创建与编辑共用。
 *
 * 【为什么合成一个组件】两种模式的字段、校验、错误出口完全一样，差别只有
 * "发给哪个端点"。分成两个文件的话，加一个字段要改两处，漏一处不报错，
 * 表现为"新建时能填、编辑时看不到"。
 *
 * 【初始值从 props 读，靠 key 重新挂载来刷新】§8：不要用 useEffect 监听
 * open 再 setState（多一次渲染，oxlint 的 react(set-state-in-effect) 也会报）。
 * 调用方每次都换 key，所以这里 useState 的初始值就是"这一次打开时的真实值"
 * ——连续编辑同一条两次也不会留上一次改到一半的内容。
 *
 * 【"绑定模型"这一项为什么是只读的】Agent 没有 per-agent 的模型绑定：
 * 一次运行用的是**全局当前生效的 chat 模型**（`internal/llm` 里 kind=chat
 * 中 createdAt 最新的那一条，见 lib/activeModel.ts 复刻的 LatestByKind，
 * 后端 `internal/agent/usecase.go` 也是这么取的）。契约的
 * CreateAgentRequest / UpdateAgentRequest 里根本没有模型字段，加一个
 * 存不进去的选择框只会让用户以为改得动。所以这里只把"现在用的是哪个"
 * 显示出来，并指向唯一能改它的地方（设置页）。
 */
export function AgentFormDialog({ open, onOpenChange, agent }: Props) {
  const { data: tools } = useToolCatalog()
  const { data: providers } = useProviders()
  const create = useCreateAgent()
  const update = useUpdateAgent()
  const showError = useErrorToast()

  const editing = agent !== undefined
  const pending = editing ? update.isPending : create.isPending
  const mutationError = editing ? update.error : create.error

  const [name, setName] = useState(agent?.name ?? '')
  const [description, setDescription] = useState(agent?.description ?? '')
  const [instruction, setInstruction] = useState(agent?.instruction ?? '')
  const [selectedTools, setSelectedTools] = useState<string[]>(agent?.toolNames ?? [])

  // 【为什么在这里回答"当前模型支不支持工具"】这个弹窗是产生"带工具的 Agent"
  // 这个决定的地方，提示放在这里最有用——放到别处等于让用户在两次导航之外
  // 才知道自己配错了。useProviders 不会多打一次请求：路由的 RequireProvider
  // 在渲染页面之前就拉过一次，缓存是热的。
  const activeModel = latestChatModel(providers ?? [])
  // 没有 chat 模型时不禁用工具勾选——那时用户连 provider 都没配好，
  // RequireProvider 会先把他带去引导页，这里再报一次"模型不支持工具"是误报。
  const toolCallingUnsupported = activeModel !== null && !activeModel.capabilities.toolCalling

  const nameError = validateName(name)
  const descriptionError = validateDescription(description)
  const instructionError = validateInstruction(instruction)
  const clientError = nameError ?? descriptionError ?? instructionError

  const toggleTool = (toolName: string, checked: boolean) => {
    setSelectedTools((prev) =>
      checked ? [...prev, toolName] : prev.filter((n) => n !== toolName),
    )
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (clientError) return

    // 【整体替换：四个字段全带上，一个都不能省】契约的 UpdateAgentRequest
    // 里四个字段全部 required，语义是"改完之后的值"——没给的按清空处理。
    // 所以这里必须把当前表单的三个值 + 工具集一起发出去，而不是只发改动过的
    // 那一个（那样会把没发的字段清掉）。
    //
    // 【为什么 description / instruction 不 trim】后端 validateAgentFields
    // 只对 name 做 TrimSpace（internal/agent/usecase.go），另外两个原样落库。
    // 在这里 trim 会悄悄改掉用户写的东西——提示词里的前导换行和缩进是有意义的。
    const body: Schemas['UpdateAgentRequest'] = {
      name: name.trim(),
      description,
      instruction,
      toolNames: selectedTools,
    }

    const onSuccess = () => onOpenChange(false)
    if (editing) {
      update.mutate({ id: agent.id, body }, { onSuccess, onError: showError })
    } else {
      create.mutate(body, { onSuccess, onError: showError })
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {/* §20：弹窗的第一行必须是标题，不能只有描述。 */}
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{editing ? '编辑 Agent' : '创建 Agent'}</DialogTitle>
            <DialogDescription>
              {editing
                ? '改完之后这些值会整体替换掉原来的配置——清空系统提示词也是在这里做的。'
                : '给它一个名字、系统提示词，选它能用哪些工具。'}
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
                disabled={pending}
              />
              {nameError && <p className="text-destructive text-sm">{nameError}</p>}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="agent-description">描述（可选）</Label>
              <Input
                id="agent-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="帮助用户做算术运算"
                disabled={pending}
              />
              {descriptionError && <p className="text-destructive text-sm">{descriptionError}</p>}
            </div>

            <div className="space-y-1.5">
              {/* 【为什么标"可选"、为什么留空不报错】见 useAgents.ts 的
                  validateInstruction：后端只查上限，且 ADK 在 instruction
                  为空时不追加 system 消息（`adk/chatmodel.go` 的
                  defaultGenModelInput 里那句 `if instruction != ""`）。
                  这里如实说"留空 = 没有系统提示词"，不说"将使用默认提示词"
                  ——后端没有这种东西，写了就是编。 */}
              <Label htmlFor="agent-instruction">系统提示词（可选）</Label>
              <textarea
                id="agent-instruction"
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
                placeholder="你是一个帮助用户做算术计算的助手，遇到需要计算的问题就调用 calculator 工具。"
                rows={4}
                disabled={pending}
                className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px]"
              />
              {instructionError ? (
                <p className="text-destructive text-sm">{instructionError}</p>
              ) : (
                <p className="text-muted-foreground text-xs">
                  留空就是"没有系统提示词"：发给模型的消息里不带 system 那一条，
                  后端不会替你补一段默认的。
                </p>
              )}
            </div>

            {/* 只读展示当前生效的模型。**不做成选择框**——契约里 Agent 没有
                模型字段，选了也存不进去（见组件头部注释）。 */}
            <div className="space-y-1.5">
              <p className="text-sm font-medium">生效的模型</p>
              <p className="text-sm">
                {activeModel ? (
                  <span className="font-medium">{activeModel.modelId}</span>
                ) : (
                  <span className="text-muted-foreground">还没有配置聊天模型</span>
                )}
              </p>
              <p className="text-muted-foreground text-xs">
                所有 Agent 共用它：每次运行用的都是当前生效的 chat 模型（同类型里
                最新配置的那一条），Agent 自己不绑定模型。要换它，去
                <Link to="/settings" className="underline">
                  设置页
                </Link>
                新增或修改模型配置。
              </p>
            </div>

            <div className="space-y-1.5">
              <p className="text-sm font-medium">可用工具</p>

              {toolCallingUnsupported && (
                <Alert variant="destructive">
                  <AlertTitle>当前聊天模型不支持工具调用</AlertTitle>
                  <AlertDescription>
                    <p>
                      生效的模型是 <span className="font-medium">{activeModel?.modelId}</span>，
                      它没有声明「支持工具调用」。勾选工具后保存会被拒绝（服务端返回
                      400 并点名这个模型）。
                    </p>
                    <p className="mt-1.5">
                      要么不勾工具，把它做成一个纯对话 Agent；要么到{' '}
                      <Link to="/settings" className="underline">
                        设置页
                      </Link>{' '}
                      新增一条声明了「支持工具调用」的模型配置。注意同类型里生效的是
                      最新的那一条。
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
                      // 【已经勾上的那一个不禁用】门控判据在后端是"这次提交的
                      // 工具集非空"（internal/agent/usecase.go 的
                      // validateAgentFields：`if len(toolNames) > 0` 才查模型
                      // 能力），**移除工具永远不需要工具调用能力**。全禁用会让
                      // "当前模型不支持工具调用、而这个 Agent 已经绑了工具"
                      // 变成死局：一个工具都取消不掉，每次保存都被 400 顶回来，
                      // 而这个表单是唯一的出口。所以只禁"没勾上的"。
                      //
                      // selectedTools 恒为空，那种条件恒为 false，等于永远放行
                      // ——它自己就把自己抵消了。零工具 Agent 是合法配置，
                      // 正是这里的逃生口。
                      disabled={
                        pending ||
                        (toolCallingUnsupported && !selectedTools.includes(tool.name))
                      }
                      onCheckedChange={(checked) => toggleTool(tool.name, checked === true)}
                    />
                    <span className="min-w-0">
                      <span className="flex flex-wrap items-center gap-1.5">
                        <span className="font-medium">{tool.name}</span>
                        <Badge variant={SIDE_EFFECT_VARIANT[tool.sideEffectLevel]}>
                          {SIDE_EFFECT_LABEL[tool.sideEffectLevel] ?? tool.sideEffectLevel}
                        </Badge>
                      </span>
                      <span className="text-muted-foreground block">{tool.description}</span>
                    </span>
                  </label>
                ))}
              </div>

              {/* 三级含义的图例。三个取值都是后端定义的，这里只解释它们
                  对用户意味着什么。 */}
              <p className="text-muted-foreground text-xs">
                只读：不改任何数据。写入（可重复）：会写，但重复执行结果一样。
                写入（不可重复）：会写且重复执行会叠加——运行被中断后，平台不会
                自动重放这类工具调用。
              </p>
            </div>

            {inlineOnly(mutationError) && (
              <Alert variant="destructive">
                <AlertTitle>{editing ? '保存失败' : '创建失败'}</AlertTitle>
                <AlertDescription>
                  <ErrorText error={mutationError} />
                </AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              取消
            </Button>
            {/* §6/§16：客户端校验不通过时按钮就禁用（省一次白跑的往返，
                真正的强制在后端），提交中改文案并禁用防重复提交。 */}
            <Button type="submit" disabled={pending || !!clientError}>
              {pending ? (editing ? '保存中…' : '创建中…') : editing ? '保存' : '创建'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
