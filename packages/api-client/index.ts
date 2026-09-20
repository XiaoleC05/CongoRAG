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

export const api = createClient<paths>({ baseUrl: "" });

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
