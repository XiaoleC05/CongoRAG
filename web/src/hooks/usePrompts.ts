import { useQuery } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { unwrap } from '@/lib/apiResult'

/**
 * Prompt 注册表的缓存 key。
 *
 * 【为什么单独导出】这一页只有读取，没有失效场景；导出常量是为了将来
 * 「改了提示词要立刻看到新版本」那种场景能找到唯一的前缀，而不是在页面里
 * 再手写一遍字面量（拼错不会报错，只会让缓存永远命中一个不存在的 key）。
 */
export const PROMPTS_KEY = ['prompts']

/**
 * 读：Prompt 审计注册表（GET /api/v1/prompts）。
 *
 * 【为什么 staleTime 是 5 分钟】这个列表的变更源是**代码发布**，不是用户操作：
 * 后端启动时装配注册表，运行期不会再改。默认 staleTime=0 会让每次切回这一页
 * 都重新请求一遍一个字节都不会变的大响应（里面是全部提示词全文）。
 * 5 分钟足够覆盖"发布后回来看一眼"的场景，又不至于让一个长期开着的页面
 * 永远看不到新部署。
 *
 * 【为什么不轮询】提示词改了要重新发版才生效——轮询一个发布级的数据是纯浪费。
 * 它和文档列表（状态真的会自己变）不是一类东西。
 */
export function usePromptCatalog() {
  return useQuery<Schemas['PromptEntry'][]>({
    queryKey: PROMPTS_KEY,
    queryFn: async (): Promise<Schemas['PromptEntry'][]> =>
      unwrap(await api.GET('/api/v1/prompts')),
    staleTime: 5 * 60 * 1000,
  })
}
