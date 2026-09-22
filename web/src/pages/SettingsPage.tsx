import { Plus, Settings as SettingsIcon } from 'lucide-react'
import { useState } from 'react'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import { AddProviderDialog } from '@/components/settings/AddProviderDialog'
import { DeleteModelDialog } from '@/components/settings/DeleteModelDialog'
import { EditModelDialog } from '@/components/settings/EditModelDialog'
import { ProviderGroup } from '@/components/settings/ProviderGroup'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { latestEmbeddingModel, useModelMutations, useProviders } from '@/hooks/useProviders'
import { useErrorToast } from '@/hooks/useErrorToast'
import { latestChatModel } from '@/lib/activeModel'
import { errorPresentation } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'

type ProviderWithModels = Schemas['ProviderWithModels']
type ModelSummary = Schemas['ModelSummary']

/**
 * 交给弹窗内联显示的那部分错误（判据见 lib/errors.ts 的 errorPresentation）。
 * 能重试的那类已经由 useErrorToast 说过了，这里再渲染一份就是同一个错误
 * 两个 role="alert"，读屏软件念两遍。
 */
const residual = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * 设置页：provider / 模型管理（issue #83）。
 *
 * 【它和引导页的分工】OnboardingPage 是"首次配置"——一个都还没有的时候
 * 挡在主界面前的那一步；这一页是"日常管理"——已经能用之后回来看配了什么、
 * 再加一条。两者共用同一批端点（`GET / POST /api/v1/providers`），
 * 所以字段、校验、换 embedding 模型的确认流程都应当一致；差别只在这一页
 * 不再挡路，也不负责"配置完就进主界面"。
 *
 * 【这一页能做什么、不能做什么（契约决定，不是取舍）】
 *   - 能：列出已配置的接入、看每个模型的 capabilities / 上下文窗口 /
 *     tokenizer、看当前生效的 embedding 模型与向量维度、新增一条接入、
 *     编辑一个模型的声明（`PATCH /api/v1/models/{id}`）、删掉一个模型
 *     （`DELETE /api/v1/models/{id}`）。
 *   - 不能：改 / 删**接入本身**（Base URL、API Key 那一层）。
 *     `/api/v1/providers` 下只有 get 和 post 两个操作，既没有 PATCH 也
 *     没有 DELETE。**没有端点就不放入口**——界面上的每个按钮背后都必须
 *     有一个真实存在的端点，做一个点了没反应的按钮，用户只会以为是自己
 *     点错了。这一条不是"暂时没做"：provider 下挂着模型与用量记录，
 *     删它会连带删掉这些，而真实需求是"换个 Key 继续用同一个 provider"，
 *     那用 POST 新增一条覆盖即可（同类的模型里生效的是最新那一条）。
 */
export default function SettingsPage() {
  const { data, isPending, error } = useProviders()
  const { update, remove } = useModelMutations()
  const showError = useErrorToast()

  const [adding, setAdding] = useState(false)
  // 每次打开弹窗都递增，用作它的 key：换 key 让 React 重新挂载组件，
  // 输入框因此拿到新的初始值（§8，比在组件里用 effect 监听 open 干净）。
  // 【编辑框尤其需要它】同一个模型连续打开两次，只按 id 做 key 的话
  // 第二次不会重新挂载，输入框里留的是上一次改到一半的值。
  const [formSeq, setFormSeq] = useState(0)

  // 弹窗状态放在页面这一层，行组件只负责"请求打开"（和知识库列表页同一个做法）。
  const [editing, setEditing] = useState<ModelSummary | null>(null)
  const [deleting, setDeleting] = useState<ModelSummary | null>(null)

  const openAdd = () => {
    setAdding(true)
    setFormSeq((n) => n + 1)
  }

  // 打开写操作弹窗前先清掉上一次的错误，否则重开时会看到已经过期的报错（§7）。
  const openEdit = (model: ModelSummary) => {
    update.reset()
    setEditing(model)
    setFormSeq((n) => n + 1)
  }
  const openDelete = (model: ModelSummary) => {
    remove.reset()
    setDeleting(model)
  }

  return (
    <div className="mx-auto max-w-4xl p-6">
      <header className="mb-6 flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">设置</h1>
          <p className="text-muted-foreground mt-1 text-sm">
            这里管的是模型接入：用哪个厂商、挂哪些模型、每个模型声明了哪些能力。
            配置存在本机，API Key 加密落盘。
          </p>
        </div>
        <Button onClick={openAdd}>
          <Plus />
          新增接入
        </Button>
      </header>

      {/* 三种状态（§2）：漏掉加载态会先闪一下空列表，漏掉错误态会得到一片
          空白。查询失败留在页内 Alert（§16），不走 toast。 */}
      {isPending ? (
        <SkeletonList />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : data.length === 0 ? (
        <EmptyState onAdd={openAdd} />
      ) : (
        <>
          <section className="mb-8">
            <h2 className="mb-3 text-base font-semibold">当前生效的模型</h2>
            <CurrentModels providers={data} />
          </section>

          <section>
            <h2 className="mb-3 text-base font-semibold">模型接入</h2>
            <div className="space-y-6">
              {groupByVendor(data).map((group) => (
                <ProviderGroup
                  key={group.vendor}
                  vendor={group.vendor}
                  providers={group.providers}
                  onEditModel={openEdit}
                  onDeleteModel={openDelete}
                  actionsDisabled={update.isPending || remove.isPending}
                />
              ))}
            </div>
            {/* 【为什么把"能改什么、不能改什么"写在界面上，而不是只在代码
                注释里】用户看到一条配错的接入，第一反应是找"改"的按钮。
                找不到时他要么以为界面还没做完，要么反复刷新。明说一句代价
                很小，省掉的是一轮"这里为什么不能改"的困惑。
                【Base URL / Key 那一层的口径】provider 本身没有 PATCH/DELETE
                （契约里 /api/v1/providers 只有 get / post），所以换 Key 的
                唯一路径是新增一条接入——这不是"暂时没做"，是有意的：
                provider 下挂着模型与用量记录，删它会连带删掉这些。 */}
            <p className="text-muted-foreground mt-4 text-xs">
              模型条目可以改（名字、能力、上下文窗口），也可以删。接入本身
              （Base URL / API Key）改不了，只能新增一条——同一类模型里
              生效的是保存时间最新的那一条。
            </p>
          </section>
        </>
      )}

      {/* 换 key 让弹窗每次打开都是一份干净的初始状态（§8）。 */}
      <AddProviderDialog
        key={`add-provider-${formSeq}`}
        open={adding}
        onOpenChange={setAdding}
      />

      <EditModelDialog
        key={`edit-model-${editing?.id ?? 'none'}-${formSeq}`}
        model={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />

      <DeleteModelDialog
        target={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        pending={remove.isPending}
        // 删除失败的两个去处和别处一致：能 toast 的由 useErrorToast 说
        // （包括"它是当前生效的那一条"那条 409），其余留在弹窗里。
        error={residual(remove.error)}
        onConfirm={() => {
          if (!deleting) return
          remove.mutate(deleting.id, {
            onSuccess: () => setDeleting(null),
            onError: showError,
          })
        }}
      />
    </div>
  )
}

/**
 * 当前生效的 chat / embedding 模型。
 *
 * 【判据是"同 kind 里 createdAt 最新的那一个"】契约里没有"当前选中"这个
 * 标记，后端按 `internal/llm/model.go` 的 `LatestByKind` 现算。前端复刻
 * 同一段（`lib/activeModel.ts` 的 `latestChatModel` + `useProviders.ts` 的
 * `latestEmbeddingModel`）——**三处必须一起改**，不跟改的后果是这里显示
 * 一个过期的模型名，不报错、只是信息是假的。
 *
 * 【向量维度为什么要单独露出来】引导页写着"这一步需要几秒钟"（探测维度），
 * 但探测结果之前没有任何地方能回看——用户没法确认"库里现在的向量列是什么
 * 形状"。issue #83 点名要补的就是这一格。
 */
function CurrentModels({ providers }: { providers: ProviderWithModels[] }) {
  const chat = latestChatModel(providers)
  const embedding = latestEmbeddingModel(providers)

  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <div className="bg-card border-border rounded-xl border p-4">
        <p className="text-muted-foreground text-xs">对话模型</p>
        {chat ? (
          <>
            <p className="mt-1 font-medium">{chat.modelId}</p>
            <p className="text-muted-foreground mt-0.5 text-xs">
              上下文 {chat.contextWindow} · 最大输出 {chat.maxOutputTokens} ·{' '}
              {formatDateTime(chat.createdAt)}
            </p>
          </>
        ) : (
          // 没有 chat 模型是可能的（比如只配了 embedding 的那一批），
          // 这时说清"没有"比显示一个空占位好。
          <p className="text-muted-foreground mt-1 text-sm">没有配置对话模型</p>
        )}
      </div>

      <div className="bg-card border-border rounded-xl border p-4">
        <p className="text-muted-foreground text-xs">Embedding 模型</p>
        {embedding ? (
          <>
            <p className="mt-1 flex items-center gap-2 font-medium">
              {embedding.modelId}
              {/* 维度是这个卡片存在的理由，给它一个显眼的徽标而不是塞进
                  下面那行小字里。 */}
              <Badge variant="secondary">{embedding.embeddingDim} 维</Badge>
            </p>
            <p className="text-muted-foreground mt-0.5 text-xs">
              向量列按这个维度建 · {formatDateTime(embedding.createdAt)}
            </p>
          </>
        ) : (
          <p className="text-muted-foreground mt-1 text-sm">没有配置 embedding 模型</p>
        )}
      </div>
    </div>
  )
}

/**
 * 按厂商分组。
 *
 * 【分组键取 baseUrl 的主机名】契约里 `ProviderWithModels` 没有厂商字段
 * （只有 baseUrl / createdAt / models），所以"厂商"是推出来的——见
 * ProviderGroup.tsx 的注释：这是近似，只影响排版。
 *
 * 【保持后端的顺序】组的顺序按某个 provider 第一次出现的顺序定，
 * 组内保持后端给的顺序。不按字母排：用户自己配的东西，"先后"比"字典序"
 * 更接近他的记忆（最新配的通常是他现在关心的）。
 *
 * 【URL 解析失败时不抛异常】baseUrl 是用户手填的，历史数据里可能有
 * 不合法的值（比如少了协议头）。`new URL` 抛异常会把整页打挂——
 * 退化成"用原始字符串当组名"，至少还能看见这条配置。
 */
function groupByVendor(
  providers: ProviderWithModels[],
): { vendor: string; providers: ProviderWithModels[] }[] {
  const groups = new Map<string, ProviderWithModels[]>()

  for (const provider of providers) {
    const vendor = hostOf(provider.baseUrl)
    const bucket = groups.get(vendor)
    if (bucket) bucket.push(provider)
    else groups.set(vendor, [provider])
  }

  return [...groups].map(([vendor, list]) => ({ vendor, providers: list }))
}

function hostOf(baseUrl: string): string {
  try {
    return new URL(baseUrl).host
  } catch {
    return baseUrl
  }
}

function SkeletonList() {
  return (
    <div className="space-y-3">
      <Skeleton className="h-24 rounded-xl" />
      {Array.from({ length: 2 }, (_, i) => (
        <Skeleton key={i} className="h-40 rounded-xl" />
      ))}
    </div>
  )
}

/**
 * 一个 provider 都没有。
 *
 * 【实际很难走到】主界面的路由守卫（router.tsx 的 RequireProvider）在
 * 一个 provider 都没有时会把人送回引导页，所以正常路径上这一页至少有
 * 一条。留着它是因为"进不来"和"进来之后崩掉"是两件事——数据真的为空时
 * 上面那张表会渲染成一片空白。留着出口（新增接入）比留一个死胡同好。
 */
function EmptyState({ onAdd }: { onAdd: () => void }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <SettingsIcon className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">还没有配置模型接入</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        加一条接入，填上你的 Base URL、Key 和模型名。
      </p>
      <Button onClick={onAdd}>
        <Plus />
        新增接入
      </Button>
    </div>
  )
}
