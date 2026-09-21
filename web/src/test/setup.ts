/**
 * vitest 的 setupFiles：每个测试文件在加载前先跑一次这里。
 *
 * 【为什么必须有它】jsdom 不实现 window.matchMedia。侧栏
 * （src/components/ui/sidebar.tsx）通过 src/hooks/use-mobile.ts 调它，
 * 而 AppLayout 里就挂着侧栏——任何渲染 AppLayout 的用例都会在 effect 里
 * 抛 "window.matchMedia is not a function"。报错点落在 use-mobile.ts，
 * 和被测的懒加载毫无关系，看半天也看不出是环境缺 API。
 * 所以这是"渲染 AppLayout 的最小前置条件"，不是顺手加的 mock。
 *
 * 【为什么 addListener / removeListener 也要给】旧版 MediaQueryList 接口
 * 只有这一对（addEventListener 是后来才加的）。现在的 use-mobile.ts 用的是
 * 新接口，但 shadcn 之后生成的组件可能还在用旧的——只补一半的话，
 * 下次换个组件会炸在同一个地方，而且同样看不出是环境问题。
 *
 * 【matches 一律 false = 视口按桌面算】手机断点是另一套分支（侧栏变成 Sheet），
 * 要测它就在具体用例里单独覆写，不要把它做成 setup 的默认值。
 */

const noop = () => {}

// 【为什么要有这个判断】setupFiles 对【所有】测试文件生效，包括不写
// `// @vitest-environment jsdom`、跑在 node 环境里的纯函数用例
// （如 src/lib/errors.test.ts）。那种文件里根本没有 window，
// 不判断就会在 import 阶段抛 "window is not defined"，
// 把一个跟浏览器无关的用例一起炸掉。
if (typeof window !== 'undefined') {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: noop,
      removeEventListener: noop,
      addListener: noop,
      removeListener: noop,
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList
}
