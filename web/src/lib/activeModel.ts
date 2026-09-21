import type { Schemas } from '@congorag/api-client'

type ProviderWithModels = Schemas['ProviderWithModels']
type ModelSummary = Schemas['ModelSummary']

/**
 * 找出「当前生效的 chat 模型」——前端版本的 internal/llm/model.go 的
 * LatestByKind。
 *
 * 【为什么前端要自己算一遍】`GET /providers` 只回显每个 provider 挂着的
 * 模型清单，没有"哪个是当前生效的"这个标记。后端判据是「同 kind 里
 * createdAt 最新的那一个」，这里复刻同一段。
 *
 * 【两边必须一起改】后端哪天把判据改成显式的 selected 标记
 * （internal/llm/model.go 的 LatestByKind），这里不跟改就会显示一个过期的
 * 模型名。横幅只是提示，不会造成数据问题——但提示错了比没有提示更糟。
 *
 * 没有 chat 模型时返回 null（调用方据此决定要不要显示提示）。
 */
export function latestChatModel(providers: ProviderWithModels[]): ModelSummary | null {
  let latest: ModelSummary | null = null

  for (const provider of providers) {
    for (const model of provider.models ?? []) {
      if (model.kind !== 'chat') continue
      // 【比时间戳而不是比字符串】createdAt 是 RFC3339 带时区的字符串，
      // 字符串比较在同一个时区偏移下碰巧成立，跨偏移就错了。
      if (latest === null || new Date(model.createdAt).getTime() > new Date(latest.createdAt).getTime()) {
        latest = model
      }
    }
  }

  return latest
}
