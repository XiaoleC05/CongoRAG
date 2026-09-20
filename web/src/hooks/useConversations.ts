import { useMutation } from '@tanstack/react-query'

import { api } from '@congorag/api-client'

/**
 * 写：新建会话。
 *
 * 【没有对应的"列表"hook】这一轮没有 GET /api/v1/conversations
 * （列出全部会话）这个端点——聊天的入口是"从知识库详情页开始对话"，
 * 不是"从一个会话列表选一个"。侧栏的"对话"导航项因此仍然标 soon，
 * 等真的要做会话浏览/管理时再补上列表端点和这个 hook。
 */
export function useCreateConversation() {
  return useMutation({
    mutationFn: async (input: { title?: string; knowledgeBaseId?: string }) => {
      const { data, error } = await api.POST('/api/v1/conversations', { body: input })
      if (error) throw error
      return data
    },
  })
}
