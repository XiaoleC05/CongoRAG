import { BarChart3, BookOpen, Bot, MessagesSquare, Settings } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

/**
 * 侧栏导航表。
 *
 * 【为什么从 AppLayout.tsx 拆出来】两个原因，第二个是硬的：
 *   1. AppLayout.test.tsx 要直接断言这张表——"标了 `soon` 的模块，src/pages/
 *      下就必须真的没有页面"。断言数据比断言渲染结果更接近这条规则本身。
 *   2. oxlint 的 react(only-export-components) 会告警：一个导出组件的文件
 *      再导出常量会打断 Fast Refresh。基线是零告警，所以常量得有自己的文件。
 *      （改 .oxlintrc.json 的 ignorePatterns 也能让它闭嘴，但那条清单是留给
 *      shadcn 生成代码的，别再往里塞手写文件。）
 */
export type NavItem = {
  to: string
  label: string
  icon: LucideIcon
  /**
   * 未实现的模块：值是一句给用户看的说明，渲染成禁用项并在 tooltip 里显示。
   * 禁用而不是链到一个空白页——让人以为点了没反应是 bug，比明说没实现更糟。
   *
   * 【这里不写里程碑编号（曾经写的是 'M2' / 'M5'）】里程碑编号是给内部排期用的，
   * 界面上的人看不到路线图，只看到"这个功能什么时候会有"。更要紧的是它会过期：
   * `/conversations` 的 'M2' 在页面落地之后就变成了假信息，而且没有任何机制会
   * 提醒摘掉它。写事实（还没做）比写计划（哪个版本做）活得久。
   */
  soon?: string
}

export const NAV: NavItem[] = [
  { to: '/knowledge-bases', label: '知识库', icon: BookOpen },
  { to: '/conversations', label: '对话', icon: MessagesSquare },
  { to: '/agents', label: 'Agent', icon: Bot },
  // 【这里的 soon 是 2026-09-22 摘掉的（issue #76）】它原来写着
  // soon: '尚未实现'，理由写的是"顺手实现它会让那条 issue 的验收无处可查"。
  // 现在 UsagePage 落地了，占位反而变成了假信息——README 的「新增一个页面」
  // 第 4 步与 AppLayout.test.tsx 的"标了 soon 的模块确实没有页面文件"
  // 一起钉着这件事：页面在，soon 就必须摘。
  { to: '/usage', label: '用量', icon: BarChart3 },
  // 【设置为什么在这里，而不在侧栏底部】侧栏底部原来是 AppLayout.tsx 里
  // 写死的一个禁用占位，它的注释把归属指向 issue #83。本批次落地了
  // SettingsPage，所以入口挪进这张表——**但底部那个占位不归本批次删**
  // （AppLayout.tsx 在改动清单之外），所以两个入口会并存一段时间。
  { to: '/settings', label: '设置', icon: Settings },
]
