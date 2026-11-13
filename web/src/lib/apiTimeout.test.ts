import { describe, expect, it, vi } from 'vitest'

import { fetchWithTimeout } from '@congorag/api-client'

describe('fetchWithTimeout', () => {
  it('超时后抛出一句中文可读错误', async () => {
    const original = globalThis.fetch
    // 构造一个"永远不返回"的请求，只对 abort 作出反应。
    globalThis.fetch = vi.fn(
      (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(init.signal?.reason))
        }),
    ) as unknown as typeof fetch
    try {
      await expect(fetchWithTimeout('/api/v1/prompts', {}, 10)).rejects.toThrow(/请求超时/)
    } finally {
      globalThis.fetch = original
    }
  })

  it('正常返回时不改动响应', async () => {
    const original = globalThis.fetch
    globalThis.fetch = vi.fn(async () => new Response('ok', { status: 200 })) as unknown as typeof fetch
    try {
      const res = await fetchWithTimeout('/api/v1/prompts', {}, 1000)
      expect(res.status).toBe(200)
      expect(await res.text()).toBe('ok')
    } finally {
      globalThis.fetch = original
    }
  })

  it('调用方自己的取消信号仍然有效', async () => {
    const original = globalThis.fetch
    globalThis.fetch = vi.fn(
      (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(init.signal?.reason))
        }),
    ) as unknown as typeof fetch
    const controller = new AbortController()
    try {
      const promise = fetchWithTimeout('/api/v1/prompts', { signal: controller.signal }, 10_000)
      controller.abort()
      await expect(promise).rejects.toBeDefined()
    } finally {
      globalThis.fetch = original
    }
  })
})
