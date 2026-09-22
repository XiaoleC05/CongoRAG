import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import { nextPageParam } from '@/lib/pagination'

/**
 * 会话列表在缓存里的 key。
 *
 * 读和写两处都用它，必须从同一个地方取——两处各写一个字面量的话，
 * 拼错一处不会报错，只会让界面"点了没反应"（和 useKnowledgeBases.ts
 * 的 KNOWLEDGE_BASES_KEY 是同一条约定）。
 */
export const CONVERSATIONS_KEY = ['conversations']

/**
 * 读：会话列表（keyset 分页，按最近活动时间倒序）。
 *
 * 【这段注释原来是"契约里没有这个端点"】现在有了：`GET /api/v1/conversations`
 * 的 `listConversations`（v4.0 补的），响应形状和其它三个列表一致——
 * `{ items, nextCursor }`，不是裸数组。
 *
 * 【为什么用户量页那套"时间窗编进 key"的做法在这里不适用】这里翻页靠的是
 * 游标，游标本身就是缓存的一部分（useInfiniteQuery 把每一页按 pageParam
 * 存在同一个 key 下）；用量页没有分页，时间窗就是它唯一的入参。
 *
 * 【排序键是"最近活动时间"不是创建时间，前端不重排】契约里写明了理由：
 * 这个列表的用处是"回到刚才那个会话"。服务端已经按它排好了，客户端再排
 * 一遍只会有两个后果——要么和游标（编码的是服务器顺序里的位置）对不上，
 * 要么白排。所以这里原样渲染，见 lib/pagination.ts 里关于游标的注释。
 */
export function useConversations() {
  return useInfiniteQuery({
    queryKey: CONVERSATIONS_KEY,
    // null 表示"从头开始"（第一页），和 nextCursor 的 nullable 语义一致，
    // getNextPageParam 里因此不必再转换。
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET('/api/v1/conversations', {
        params: { query: { cursor: pageParam ?? undefined } },
      })
      if (error) throw error
      return data
    },
    // hasNextPage 由它算出来：nextCursor 为 null / 缺省 = 到底了。
    // 「加载更多」按钮据此整个不渲染，而不是渲染成禁用（§16）。
    getNextPageParam: nextPageParam,
  })
}

/**
 * 写：新建会话。
 *
 * 【入参比契约窄一格】契约的 `CreateConversationRequest.knowledgeBaseId`
 * 是 `string | null`（允许显式传 null），这里收窄成 `string | undefined`——
 * 前端只有"关联一个知识库"和"不关联"两种意图，没有"显式传 null"这种第三种。
 * 两者不一致是既有的，跟着契约类型走编译不过，所以按 hook 自己的签名写。
 */
export function useCreateConversation() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (input: { title?: string; knowledgeBaseId?: string }) => {
      const { data, error } = await api.POST('/api/v1/conversations', { body: input })
      if (error) throw error
      return data
    },
    // 【这句 invalidate 是 v4.0 补上的（issue #50 的遗留）】上一轮没有列表
    // 端点，所以"写操作作废缓存"在这一处没有可失效的对象，注释里挂账说
    // "等那个端点补上之后再写"。现在列表有了：不失效的话，从会话列表页
    // 新建一个会话，跳进去再回来，列表里看不到它——数据改成功了但页面没刷新，
    // 而且【不报错】（§3）。
    //
    // 【为什么不像 useCreateProvider 那样写穿缓存】那个 hook 写穿是因为
    // 服务端返回的整条 provider（含 models）就是列表的完整元素；这里返回的
    // Conversation 也在列表元素里，但列表是**分页**的——新的会话按"最近活动
    // 时间"排在第一页第一条，而用户可能已经加载了好几页。往缓存头部插一条
    // 会让"加载更多"的游标错位。作废一次让服务端重新给第一页，才是对的。
    onSuccess: () => queryClient.invalidateQueries({ queryKey: CONVERSATIONS_KEY }),
  })
}

/**
 * 写：删除会话（级联硬删消息、事件、摘要）。
 *
 * 【这个端点原来不存在，是 2026-09-22 和本批次并行落地的】本批次开始时
 * `contracts/openapi.yaml` 的 `/api/v1/conversations` 下只有 get / post，
 * 会话列表因此只能做"列出 / 切换 / 新建"三件事，删除入口是**故意留空**的
 * ——没有端点就不放按钮。写这份代码时契约已经补上了
 * `deleteConversation`（`/api/v1/conversations/{id}` 的 delete），
 * 所以入口补上了。
 *
 * 【为什么是硬删】契约的 description 写明了：级联硬删，不留软删状态
 * （软删会让"删了还在被检索到"这种事发生）。这意味着**不可逆**，
 * 调用方必须用 AlertDialog 确认（§20），不能弹一个普通的 Dialog。
 *
 * 【为什么单独一个 hook，而不是合成 useConversationMutations】见上面
 * useCreateConversation 的注释：那个已经有调用方在别的文件里，而那个文件
 * 不在本批次的改动清单里——合成会变成一次跨文件改名。等它能改了再合并。
 */
export function useDeleteConversation() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/api/v1/conversations/{id}', {
        params: { path: { id } },
      })
      if (error) throw error
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: CONVERSATIONS_KEY }),
  })
}
