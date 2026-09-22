import { MoreHorizontal, Pencil, Trash2 } from 'lucide-react'

import type { Schemas } from '@congorag/api-client'

import { CapabilityBadges } from '@/components/settings/CapabilityBadges'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { formatDateTime } from '@/lib/format'

type ProviderWithModels = Schemas['ProviderWithModels']
type ModelSummary = Schemas['ModelSummary']

type Props = {
  /** 厂商名。由 SettingsPage 从 baseUrl 的主机名推出来（契约里没有这个字段）。 */
  vendor: string
  providers: ProviderWithModels[]
  onEditModel: (model: ModelSummary) => void
  onDeleteModel: (model: ModelSummary) => void
  /** 有写操作在飞时禁用行尾菜单，避免连点。 */
  actionsDisabled: boolean
}

/**
 * 一个厂商分组：分组标题 + 它下面每一条接入。
 *
 * 【"厂商"是从 baseUrl 推出来的，不是契约里的字段】`ProviderWithModels`
 * 只有 `{ id, baseUrl, createdAt, models }`——没有 name / vendor。界面上要
 * "厂商分组"（界面引用方案 §2.1），只能拿 URL 的主机名当这个标签。
 * 这是**推断，不是事实**：同一个厂商的多个端点（比如自建的网关和官方域名）
 * 会被分成两组。分组只影响排版，不影响任何数据，所以这个近似是可以接受的；
 * 契约哪天补了 vendor 字段，改的是 SettingsPage 里那一个函数。
 */
export function ProviderGroup({
  vendor,
  providers,
  onEditModel,
  onDeleteModel,
  actionsDisabled,
}: Props) {
  return (
    <section className="space-y-3">
      {/* h3：它在 h1（设置）下、h2（模型接入）的下一层，不跳级（§12）。 */}
      <h3 className="text-muted-foreground text-sm font-medium">{vendor}</h3>
      <div className="space-y-3">
        {providers.map((provider) => (
          <ProviderBlock
            key={provider.id}
            provider={provider}
            onEditModel={onEditModel}
            onDeleteModel={onDeleteModel}
            actionsDisabled={actionsDisabled}
          />
        ))}
      </div>
    </section>
  )
}

/**
 * 一条模型接入：端点信息 + 它挂着的模型条目。
 *
 * 【这一层没有编辑 / 删除按钮，这是端点决定的】契约里 `/api/v1/providers`
 * 下只有 `listProviders`（get）和 `createProvider`（post）——没有 PATCH、
 * 没有 DELETE。**没有端点就不放入口**：一个点了没反应的按钮比没有按钮更糟，
 * 用户会以为是自己点错了，反复点。
 *
 * 能改能删的粒度是**模型条目**（`PATCH / DELETE /api/v1/models/{id}`），
 * 所以操作入口在下面每一行的行尾。要换 Base URL / Key，就新增一条接入
 * （同类的模型里生效的是 createdAt 最新的那一条，见
 * internal/llm/model.go 的 LatestByKind）。
 */
function ProviderBlock({
  provider,
  onEditModel,
  onDeleteModel,
  actionsDisabled,
}: {
  provider: ProviderWithModels
  onEditModel: (model: ModelSummary) => void
  onDeleteModel: (model: ModelSummary) => void
  actionsDisabled: boolean
}) {
  return (
    <div className="bg-card border-border overflow-hidden rounded-xl border">
      <div className="border-border flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        {/* 端点地址原样显示（等宽字体）：用户需要拿它和后端的配置对账，
            做任何"美化"（去掉协议、截断）都会让对账变成猜。 */}
        <code className="truncate font-mono text-xs">{provider.baseUrl}</code>
        <span className="text-muted-foreground shrink-0 text-xs">
          保存于 {formatDateTime(provider.createdAt)}
        </span>
      </div>

      {/* 【空模型列表也要说一句】models 是 required 但是数组，理论上可以为空
          （后端建 provider 时会同时建三行，实际不会空），渲染成一张空表格
          看起来像坏了。 */}
      {(provider.models ?? []).length === 0 ? (
        <p className="text-muted-foreground px-4 py-3 text-sm">这条接入下没有模型。</p>
      ) : (
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-muted-foreground text-left">
            <tr>
              <th className="px-4 py-2 font-medium">模型</th>
              <th className="px-4 py-2 font-medium">类型</th>
              <th className="px-4 py-2 text-right font-medium">上下文窗口</th>
              <th className="px-4 py-2 text-right font-medium">最大输出</th>
              <th className="px-4 py-2 font-medium">Tokenizer</th>
              <th className="px-4 py-2">
                {/* 操作列的表头留空，但读屏软件需要知道这一列是什么。 */}
                <span className="sr-only">操作</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {(provider.models ?? []).map((model) => (
              <tr key={model.id} className="border-border border-t align-top">
                <td className="px-4 py-2">
                  <p className="font-medium">{model.modelId}</p>
                  {/* capabilities 只在 chat 模型上有意义（契约里四个开关的
                      注释写明了："由服务端根据这个模型是 chatModel 还是
                      embeddingModel 自动决定"）。embedding 行显示一排灰掉的
                      开关只会让人以为"这个模型被禁用了工具调用"。 */}
                  {model.kind === 'chat' && (
                    <div className="mt-1.5">
                      <CapabilityBadges capabilities={model.capabilities} />
                    </div>
                  )}
                </td>
                <td className="px-4 py-2">
                  <ModelKindBadge model={model} />
                </td>
                <td className="text-muted-foreground px-4 py-2 text-right">
                  {model.contextWindow}
                </td>
                <td className="text-muted-foreground px-4 py-2 text-right">
                  {model.maxOutputTokens}
                </td>
                <td className="text-muted-foreground px-4 py-2 font-mono text-xs">
                  {model.tokenizerType}
                </td>
                <td className="px-4 py-2 text-right">
                  {/* 【每行最多一个主操作，其余进溢出菜单（§19）】"编辑"是
                      主操作，破坏性的"删除"收进 `···` 并用 destructive 样式
                      + 分隔线（形状照 DocumentRowActions）。
                      【可访问名带上模型名（§14）】表格里一行一个 `···`，
                      读屏用户听到一串"操作"等于没说。 */}
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`${model.modelId} 的操作`}
                        disabled={actionsDisabled}
                      >
                        <MoreHorizontal className="text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="min-w-40">
                      <DropdownMenuItem onSelect={() => onEditModel(model)}>
                        <Pencil />
                        编辑
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" onSelect={() => onDeleteModel(model)}>
                        <Trash2 />
                        删除
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

/**
 * 类型徽标。embedding 行额外把维度写在徽标里。
 *
 * 【维度为什么写在这里、而不是单独一列】契约里 `embeddingDim` 对 chat 模型
 * 恒为 0（注释写明）。单独一列的话，chat 行会显示一个毫无意义的 0，
 * 而 0 和"探测出来的维度是 0"不可区分。写在 embedding 的徽标里，
 * 这一格就是"向量化 · 1024 维"，chat 行则是干净的一个"对话"。
 */
function ModelKindBadge({ model }: { model: ModelSummary }) {
  if (model.kind === 'embedding') {
    return (
      <Badge variant="outline">
        向量化 · {model.embeddingDim} 维
      </Badge>
    )
  }
  return <Badge variant="outline">对话</Badge>
}
