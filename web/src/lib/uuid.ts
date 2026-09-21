/**
 * 生成一个幂等键。
 *
 * 【为什么不用 `crypto.randomUUID()` 一步到位】它在安全上下文里才有：
 * 生产是 http://127.0.0.1（回环算安全上下文，所以有），但 vitest 跑的
 * jsdom 不保证提供它——测试里会得到 `crypto.randomUUID is not a function`。
 * 退路是自己用 getRandomValues 拼一个同样随机、同样够长的字符串。
 *
 * 【键的强度要求不高】它只需要在「同一个会话 + 同一个键」这个作用域里
 * 不撞车。22 字节随机即 176 位，远超任何合理需求。
 *
 * 【调用点的规则，见 useMessages.ts】一次用户意图 = 一个键：新按下发送
 * 是一个新键；将来若加自动重试，同一个 attempt 必须复用同一个键。
 * 每次重试都换新键的话，幂等就完全失效了——那正是这个功能要防的事。
 */
export function newIdempotencyKey(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') {
    return c.randomUUID()
  }

  const bytes = new Uint8Array(22)
  c.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}
