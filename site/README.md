# congorag-site

CongoRAG 的静态站点：Astro 6 + Starlight。

## 本地构建

```bash
pnpm install
pnpm build
pnpm preview
```

需要 Node.js 22.12 或更高版本。站点是纯静态产物，构建结果在 `dist/`。

## 结构

- `src/pages/index.astro`：落地页
- `src/content/docs/docs/`：文档
- `src/styles/site.css`：落地页样式
- `src/styles/starlight.css`：文档主题
- `public/`：图标、截图、OG 图
- `astro.config.mjs`：Starlight 配置和侧边栏

文档的标题、描述和侧边栏顺序在 `astro.config.mjs` 里维护。新增文档时两处都要改，否则构建会报 sidebar slug 不存在。

## 写作

写面向用户的文案前先读 [`../ops/EDITORIAL.md`](../ops/EDITORIAL.md)。只写已经实现的功能，不写没有测过的数字，不把 README 的自述当成事实。

## 部署

站点由 `.github/workflows/site-release.yml` 构建成 `site.tar.gz`，和 `congorag-daily` 二进制一起挂到 Release。服务器只下载产物，不在服务器上编译。
