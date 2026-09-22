/**
 * 写剪贴板（issue #89）。
 *
 * 【为什么单独成一个文件】代码块和整条回答两处都要复制，而"失败了长什么样"
 * 两边必须一致。按 web/README.md 的"放哪的判据"，这里是薄薄一层浏览器 API
 * 封装（和 lib/streamChat.ts 同一类东西），组件只负责调用。
 *
 * 【为什么失败抛的是 Problem 形状的对象】项目里所有错误的呈现都由
 * lib/errors.ts 决定（errorMessage / errorDetail / errorPresentation），
 * 组件不许自己判断。剪贴板失败当然不是后端返回的 RFC 7807，但这里构造一个
 * 同形状的对象，就能原样走那条呈现链路，不用另开一套。
 *
 * 【type 为什么不加进 lib/errors.ts 的 MESSAGES】那张表与后端
 * apps/api/internal/api/problem.go 的 classify() 一一对应（README 的错误约定
 * 里写明了），塞一个纯前端的 type 进去会破坏这个对应关系，而且没人会想到
 * 去后端找它。用一个表里没有的 type：errorMessage 会回退到 detail，
 * 而 detail 正是要给用户看的那句话——这条回退路径本身有测试钉着
 * （ErrorToast.test.tsx 的"认不出的 type 回退到后端原文"）。
 *
 * 【为什么文案里要带"怎么做"】剪贴板失败只有两种原因，用户能采取的行动
 * 完全不同：非安全上下文是"这个页面根本给不了你剪贴板"，权限被拒是
 * "去站点设置里放开"。只说一句"复制失败"，用户没有任何下一步。
 */

/** 浏览器根本没有 navigator.clipboard（非安全上下文、旧浏览器）。 */
const CLIPBOARD_UNAVAILABLE = 'clipboard_unavailable'

/** 有 API，但 writeText 被拒绝（用户拒绝授权、页面不在焦点中）。 */
const CLIPBOARD_DENIED = 'clipboard_denied'

export async function writeClipboard(text: string): Promise<void> {
  // 【必须显式判断，不能靠 try/catch】非安全上下文（http，且不是 localhost）
  // 下 navigator.clipboard 是 undefined，不是"调用会抛"。直接
  // navigator.clipboard.writeText(...) 抛的是 TypeError: Cannot read
  // properties of undefined —— 用户看到的是一句和剪贴板毫无关系的报错。
  // 交付期前端由 Go 内嵌在 :3210 上提供，默认就是 http，这条路径是真会走到的。
  if (typeof navigator === 'undefined' || !navigator.clipboard) {
    throw {
      type: CLIPBOARD_UNAVAILABLE,
      title: '复制失败',
      detail: '当前页面不提供剪贴板 API（需要 HTTPS 或 localhost）',
    }
  }

  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // 用户点了"拒绝"、浏览器策略拒绝（页面不在前台）都会落到这里。
    // 【不能静默】复制是"界面上没有证据"的操作：不做回执，用户分不清
    // "复制成功了"和"点了没反应"，只能回去重新划选一遍。
    throw {
      type: CLIPBOARD_DENIED,
      title: '复制失败',
      detail: '浏览器拒绝了剪贴板访问，请检查本站的剪贴板权限',
    }
  }
}
