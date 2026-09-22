import type { Schemas } from '@congorag/api-client'

import { Badge } from '@/components/ui/badge'

type Capabilities = Schemas['Capabilities']

/**
 * 能力开关的顺序与文案。
 *
 * 【为什么用数组而不是直接遍历对象】对象的键顺序是隐式的——加一位能力时
 * 它可能出现在任何位置，而且没人会发现。显式排一遍，"四个开关按同一个
 * 顺序出现在每一行"这件事才是有保证的。
 */
const CAPABILITY_LABELS: { key: keyof Capabilities; label: string }[] = [
  { key: 'chat', label: '对话' },
  { key: 'streaming', label: '流式' },
  { key: 'toolCalling', label: '工具调用' },
  { key: 'reasoning', label: '推理' },
]

/**
 * 一个模型的 capabilities 回显（issue #83 的"能回看 capabilities"）。
 *
 * 【关掉的那几位也要显示出来，不能只显示开着的】只显示开着的，用户没法
 * 区分"这个模型不支持工具调用"和"这一位没人填过"——而这两种情况在这里
 * 的后果完全不同（前者会让带工具的 Agent 创建失败，见 issue #38 的门控）。
 * 关掉的那几位用 outline + 删除线，视觉上退到后面，但信息还在。
 *
 * 【不区分 chat / embedding：embedding 模型的 capabilities 服务端全给 false】
 * 契约里只有 chat 模型会用到这四个开关，调用方（ProviderCard）负责决定
 * 要不要渲染这一行。
 */
export function CapabilityBadges({ capabilities }: { capabilities: Capabilities }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {CAPABILITY_LABELS.map(({ key, label }) => {
        const on = capabilities[key]
        return (
          <Badge key={key} variant={on ? 'secondary' : 'outline'}>
            <span className={on ? undefined : 'text-muted-foreground line-through'}>
              {label}
            </span>
          </Badge>
        )
      })}
    </div>
  )
}
