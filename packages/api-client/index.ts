// 类型安全的 API 客户端。
//
// 这个包里两样东西都来自契约：src/schema.d.ts 由 openapi-typescript
// 从 contracts/openapi.yaml 生成，下面的 api 是 openapi-fetch 吃那份类型产出的请求器。
//
// baseUrl 是空字符串：路径在契约里就是完整的（/api/v1/knowledge-bases），
// 不需要前缀。请求发的是相对路径，所以两种环境都不用改代码：
//
//   开发期：页面在 :5173，请求打到 Vite dev server，由 vite.config.ts 的 proxy 转发
//   交付期：前端由 :3210 内嵌提供，同源，直接命中

import createClient from "openapi-fetch";

import type { components, paths } from "./src/schema";

/**
 * 非流式请求的默认超时。
 *
 * 【为什么必须有】没有超时的 fetch 在"TCP 还连着但服务端不再回包"的场景下
 * 会永远 pending：浏览器不报错、界面一直转圈、用户以为在生成。120s 覆盖
 * 最慢的合法请求（写作 Copilot / 引用综述是一次完整生成），超过它基本可以
 * 判定连接已死。
 *
 * 【为什么不管 SSE】流式回答走 web/src/lib/streamChat.ts 自己的 fetch +
 * ReadableStream：它需要"长时间不设上限，但空闲时靠心跳保活"，语义与这里
 * 的一次性请求相反，不能共用同一个超时。
 */
export const DEFAULT_TIMEOUT_MS = 120_000;

/** 把两个可能为空的 AbortSignal 合成一个；旧运行环境没有 AbortSignal.any 时退化为超时信号。 */
function combineSignals(existing: AbortSignal | null | undefined, timeout: AbortSignal): AbortSignal {
  if (!existing) return timeout;
  if (typeof AbortSignal.any === "function") return AbortSignal.any([existing, timeout]);
  return timeout;
}

/**
 * 带超时的 fetch。导出它是为了可单测（真造一个 120s 的挂起请求不现实）。
 *
 * 超时抛 TimeoutError 形状的错误，调用方（lib/apiResult 的 unwrap 之外那层）
 * 可以把它和"后端返回 4xx"区分开：前者是网络/连接层失败，后者是业务错误。
 */
export async function fetchWithTimeout(
  input: RequestInfo | URL,
  init?: RequestInit,
  timeoutMs: number = DEFAULT_TIMEOUT_MS,
): Promise<Response> {
  const timeout = AbortSignal.timeout(timeoutMs);
  // 【为什么还要看 input.signal】openapi-fetch 的 fetch 接口只有一个 Request
  // 参数，调用方的取消信号挂在 Request 上而不是 init 里；漏掉它会让"用户点
  // 取消"退化成"等满 120 秒"。
  const existing = init?.signal ?? (input instanceof Request ? input.signal : null);
  try {
    return await fetch(input, { ...init, signal: combineSignals(existing, timeout) });
  } catch (err) {
    const name = (err as { name?: string } | null)?.name;
    if (name === "TimeoutError" || name === "AbortError") {
      throw new Error(`请求超时（${Math.round(timeoutMs / 1000)}s）：服务端没有在限期内响应`);
    }
    throw err;
  }
}

export const api = createClient<paths>({
  baseUrl: "",
  // openapi-fetch 的 fetch 接口只传一个 Request（信号挂在它身上），
  // 所以这里不再接 init。
  fetch: (request: Request) => fetchWithTimeout(request),
});

export type { paths, components, operations } from "./src/schema";

/**
 * 契约里 components/schemas 下所有数据类型的快捷入口。
 *
 * 页面里要用某个类型时写：
 *
 *   import type { Schemas } from "@congorag/api-client";
 *   type KnowledgeBase = Schemas["KnowledgeBase"];
 *
 * 不要自己手写这些结构体——它们必须和 contracts/openapi.yaml 一致。
 */
export type Schemas = components["schemas"];
