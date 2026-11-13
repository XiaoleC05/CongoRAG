import { ScrollText } from 'lucide-react'
import { useState } from 'react'
import type { ReactNode } from 'react'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import { CopyButton } from '@/components/conversation/CopyButton'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { usePromptCatalog } from '@/hooks/usePrompts'
import { formatDateTime } from '@/lib/format'

type PromptEntry = Schemas['PromptEntry']
type PromptVersion = Schemas['PromptVersion']

/**
 * Prompt 审计页（R2 赛事特色）。
 *
 * 【这一页为什么存在】决赛答辩要求"Prompt 审计记录"。在这之前那份记录是
 * `docs/prompt-audit.md` 里的一张手写表格——它的问题不是写得不好，而是
 * **和代码没有连接**：改了提示词没人记得回来改表格，表格也不会告诉读者
 * "线上跑的是哪一版"。这一页把注册表（后端启动时装配）直接摊开：
 * 版本、内容哈希、变更原因、变量、代码位置全部来自同一份运行时数据。
 *
 * 【为什么不做编辑/发布】提示词是**代码资产**：改动要走代码评审、跟着发布
 * 一起上线，哈希才有意义。给页面加一个"在线改提示词"的编辑框会让"审计"
 * 退化成"随便试试"，而且需要另一套权限与回滚机制。这一页只读。
 *
 * 【为什么卡片不是表格】每条 Prompt 的内容是一整段多行文本 + 若干结构化
 * 元信息，表格里塞不下；两列卡片在桌面上能并排比对两条提示词，窄屏单列
 * 也不会出现"被裁掉的列"（README §15/§19 的老问题）。
 */
export default function PromptsPage() {
  const { data, isPending, error } = usePromptCatalog()

  return (
    <div className="mx-auto max-w-6xl p-6">
      <header className="mb-6">
        <h1 className="text-xl font-semibold">Prompt 审计</h1>
        {/* 这句话是这一页的存在理由，不是装饰文案——没有它，读者会把这一页
            当成"又一个设置页"，而看不出它解决的是"提示词不可审计"。 */}
        <p className="text-muted-foreground mt-1 max-w-3xl text-sm">
          提示词不是藏在代码里的字符串，而是带版本、哈希与变更原因的可审计资产。
        </p>
      </header>

      {isPending ? (
        <SkeletonCatalog />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : data.length === 0 ? (
        <EmptyState />
      ) : (
        <>
          <p className="text-muted-foreground mb-3 text-xs">
            注册表共 {data.length} 个条目。内容哈希是 SHA-256：用它核对线上实际生效的
            版本，比"去看代码里的字符串"可靠——字符串会漂，哈希不会。
          </p>
          <div className="grid items-start gap-4 lg:grid-cols-2">
            {data.map((entry) => (
              <PromptCard key={entry.key} entry={entry} />
            ))}
          </div>
        </>
      )}
    </div>
  )
}

/**
 * 一条 Prompt 的审计卡片。
 *
 * 【为什么版本切换是卡片内的局部 state】切版本是"我在比对历史"，不是一次
 * 新的数据请求——契约把一个条目的全部版本放在同一个响应里下发。把选中版本
 * 放进 query key 或 URL，会让"点一下 v1 看看"变成一次网络往返，还会把
 * 浏览历史弄脏。所以这里只换展示内容，不重新请求。
 *
 * 【为什么默认选 activeVersion 而不是最新版本号】两者通常相同，但契约里
 * "编号最大"不保证"当前生效"（回滚过的条目就是反例）。审计页默认要展示的
 * 是"现在真正在用的那一版"，头部徽标再单独标出 activeVersion。
 */
function PromptCard({ entry }: { entry: PromptEntry }) {
  const [selectedVersion, setSelectedVersion] = useState(entry.activeVersion)

  const versions = [...entry.versions].sort((a, b) => a.version - b.version)
  const selected: PromptVersion | undefined =
    versions.find((v) => v.version === selectedVersion) ??
    versions.find((v) => v.version === entry.activeVersion) ??
    versions[0]

  const samplingText = selected ? samplingSummary(selected.sampling) : null

  return (
    <article className="bg-card border-border flex flex-col gap-4 rounded-xl border p-4">
      <header className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-base font-semibold">{entry.title}</h2>
          <p className="text-muted-foreground mt-0.5 font-mono text-xs">{entry.key}</p>
        </div>
        <Badge variant="secondary" className="shrink-0">
          Active v{entry.activeVersion}
        </Badge>
      </header>

      <p className="text-muted-foreground text-sm">{entry.description}</p>

      {selected === undefined ? (
        <p className="text-muted-foreground text-sm">这个条目没有版本记录。</p>
      ) : (
        <>
          {/* 只有一个版本时整块不渲染，而不是渲染成一个禁用的按钮——
              禁用的按钮会让人以为"等一下还有"，而历史版本永远不会多出来。 */}
          {versions.length > 1 && (
            <div
              role="group"
              aria-label={`${entry.title} 的版本`}
              className="flex flex-wrap items-center gap-1.5"
            >
              {versions.map((version) => {
                const isSelected = version.version === selected.version
                return (
                  <Button
                    key={version.version}
                    type="button"
                    size="sm"
                    variant={isSelected ? 'default' : 'outline'}
                    aria-pressed={isSelected}
                    onClick={() => setSelectedVersion(version.version)}
                  >
                    v{version.version}
                  </Button>
                )
              })}
            </div>
          )}

          <div>
            <div className="flex items-center justify-between gap-2">
              <p className="text-muted-foreground text-xs">
                当前版本内容 · v{selected.version} · 生效于 {formatDateTime(selected.activatedAt)}
              </p>
              {/* 复制按钮放在内容区右上角：它复制的是**当前选中的版本**，
                  和正文在同一行才看得出作用对象。 */}
              <CopyButton
                text={selected.content}
                label={`复制「${entry.title}」提示词内容`}
              />
            </div>
            <pre className="bg-muted/40 border-border mt-2 max-h-72 overflow-auto rounded-lg border p-3 font-mono text-xs whitespace-pre-wrap break-words">
              {selected.content}
            </pre>
          </div>

          <dl className="space-y-2">
            <InfoItem label="内容哈希（SHA-256）">
              {/* 只显示前 12 位：完整哈希 64 位在卡片里会把布局撑破；完整值
                  放 title 里，鼠标悬停/读屏也能拿到，复制需求由正文旁的
                  CopyButton 承担的是内容而不是哈希。 */}
              <code className="font-mono text-xs" title={selected.contentHash}>
                {selected.contentHash.slice(0, 12)}…
              </code>
            </InfoItem>
            <InfoItem label="变更原因">{selected.changeReason || '（未填写）'}</InfoItem>
            {samplingText !== null && <InfoItem label="采样参数">{samplingText}</InfoItem>}
            {selected.metrics && (
              <InfoItem label="指标">
                {selected.metrics.name}：{selected.metrics.value}
                {selected.metrics.sampleSize !== undefined &&
                  `（样本 ${selected.metrics.sampleSize}）`}
                {selected.metrics.note ? ` — ${selected.metrics.note}` : ''}
              </InfoItem>
            )}
          </dl>

          <div>
            <p className="text-muted-foreground text-xs font-medium">变量</p>
            {entry.variables.length === 0 ? (
              <p className="text-muted-foreground mt-1 text-sm">
                无变量（内容固定，不需要运行时填充）
              </p>
            ) : (
              <ul className="mt-1 space-y-1">
                {entry.variables.map((variable) => (
                  <li key={variable.name} className="flex flex-wrap items-baseline gap-2">
                    <code className="bg-muted rounded px-1 font-mono text-xs">{variable.name}</code>
                    <Badge variant={variable.required ? 'secondary' : 'outline'}>
                      {variable.required ? '必填' : '可选'}
                    </Badge>
                    <span className="text-muted-foreground text-xs">{variable.description}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div>
            <p className="text-muted-foreground text-xs font-medium">代码位置</p>
            {entry.codeRefs.length === 0 ? (
              <p className="text-muted-foreground mt-1 text-sm">未登记引用位置。</p>
            ) : (
              <ul className="mt-1 space-y-1">
                {entry.codeRefs.map((ref) => (
                  <li key={`${ref.file}:${ref.symbol}`}>
                    <code className="text-xs break-all">
                      {ref.file}:{ref.symbol}
                    </code>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </>
      )}
    </article>
  )
}

/**
 * 采样参数的展示文本。
 *
 * 【没有参数时返回 null，整行不渲染】渲染成"未配置"只会给卡片加一行噪音——
 * "这条提示词没调采样参数"本身不是需要审计的信息，真正的信息是调了的时候
 * 调成了多少。
 */
function samplingSummary(sampling?: Schemas['PromptSampling']): string | null {
  if (!sampling) return null
  const parts: string[] = []
  if (sampling.temperature !== undefined) parts.push(`temperature ${sampling.temperature}`)
  if (sampling.maxTokens !== undefined) parts.push(`maxTokens ${sampling.maxTokens}`)
  if (sampling.note) parts.push(sampling.note)
  return parts.length > 0 ? parts.join(' · ') : null
}

function InfoItem({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      <dt className="text-muted-foreground text-xs sm:w-36 sm:shrink-0">{label}</dt>
      <dd className="min-w-0 text-sm">{children}</dd>
    </div>
  )
}

function SkeletonCatalog() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-4 w-64" />
      <div className="grid items-start gap-4 lg:grid-cols-2">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-80 rounded-xl" />
        ))}
      </div>
    </div>
  )
}

/**
 * 空态。这里没有"去创建一个"的出口——注册表由后端装配，前端没有创建入口。
 * 空只说明这次构建没有注册任何提示词（走错环境/装配失败），所以文案要把
 * 排查方向说清楚，而不是给一个点了没用的按钮。
 */
function EmptyState() {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <ScrollText className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">注册表里还没有条目</p>
      <p className="text-muted-foreground mt-1 text-sm">
        Prompt 注册表在后端启动时装配。为空说明这次构建没有注册任何提示词，
        检查一下服务端日志与注册表配置。
      </p>
    </div>
  )
}
