import type { Schemas } from '@congorag/api-client'

/**
 * run 与 step 的状态枚举。
 *
 * 【从契约取，不手写】README §5：手写的结构会悄悄过期。这里连"有几态"
 * 都由 `contracts/openapi.yaml` 决定。
 *
 * 【step 的状态是它的子集】`AgentRunStep.status` 少了 `cancelled`——取消
 * 发生在 run 层，没跑完的那一步留 `interrupted`（契约里写明了）。所以
 * 徽章组件收 run 的状态类型，step 的值天然能传进来。
 */
export type RunStatus = Schemas['AgentRun']['status']

/**
 * 六态的中文映射。**全站唯一一份**（README §17）。
 *
 * 【为什么是 Record<RunStatus, string> 而不是 Record<string, string>】
 * 契约加了第七态、或哪一态被改名时，这里会直接编译不过。用宽松的索引
 * 签名则会静默漏掉一档——界面上那一行什么也不显示，也不报错，正是这一
 * 节想防的事。
 *
 * 【interrupted 不是不可达的】v4.0 起崩溃扫描与客户端断开都会真的把它
 * 写进库，所以它必须有和 failed 一样明确的呈现，不能落进兜底样式。
 */
export const RUN_STATUS_LABEL: Record<RunStatus, string> = {
  pending: '排队中',
  running: '运行中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '已中断',
}

/**
 * 徽章配色。
 *
 * 【为什么 cancelled 和 failed 要分开】两者对用户的含义完全不同：cancelled
 * 是他自己按的取消，failed 是出错了（这条区分在后端也是显式做的——cancel
 * 端点写 cancelled，客户端断开写 interrupted，见 internal/agent 的 runningRun）。
 * 都染成红色等于每次取消都报一次假警。
 *
 * 【为什么用 variant 而不是自己写类名】颜色只能走语义 token 与既有变体
 * （README §9），自己拼 bg-red-500 之类在浅色模式下就废了。
 */
export const RUN_STATUS_VARIANT: Record<RunStatus, 'default' | 'secondary' | 'destructive' | 'outline'> =
  {
    pending: 'outline',
    running: 'secondary',
    completed: 'default',
    failed: 'destructive',
    cancelled: 'outline',
    interrupted: 'destructive',
  }
