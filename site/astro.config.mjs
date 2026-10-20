import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://oxelia51.com',
  trailingSlash: 'always',
  integrations: [
    starlight({
      title: 'CongoRAG',
      description: '本地优先的 RAG 问答与 Agent 平台。',
      defaultLocale: 'zh-CN',
      locales: { root: { label: '简体中文', lang: 'zh-CN' } },
      favicon: '/favicon.svg',
      logo: { src: './src/assets/logo-mark.svg' },
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/XiaoleC05/CongoRAG' }],
      editLink: {
        baseUrl: 'https://github.com/XiaoleC05/CongoRAG/edit/main/site/',
      },
      lastUpdated: true,
      pagination: true,
      sidebar: [
        {
          label: '开始',
          items: [
            { label: 'CongoRAG 是什么', slug: 'docs' },
            { label: '快速开始', slug: 'docs/quickstart' },
            { label: '配置', slug: 'docs/configuration' },
            { label: '常见问题', slug: 'docs/faq' },
          ],
        },
        {
          label: '使用',
          items: [
            { label: '接入模型', slug: 'docs/guides/models' },
            { label: '知识库与文档', slug: 'docs/guides/knowledge-bases' },
            { label: '对话与引用', slug: 'docs/guides/chat' },
            { label: 'Agent', slug: 'docs/guides/agents' },
            { label: '用量', slug: 'docs/guides/usage' },
          ],
        },
        {
          label: '部署',
          items: [
            { label: '从源码运行', slug: 'docs/deploy/source' },
            { label: 'Docker 交付形态', slug: 'docs/deploy/docker' },
            { label: '反向代理与安全', slug: 'docs/deploy/reverse-proxy' },
            { label: '升级与回滚', slug: 'docs/deploy/upgrade' },
          ],
        },
        {
          label: '技术细节',
          items: [
            { label: '整体架构', slug: 'docs/technical/architecture' },
            { label: '数据与存储', slug: 'docs/technical/data' },
            { label: '检索', slug: 'docs/technical/retrieval' },
            { label: '流式协议', slug: 'docs/technical/sse' },
            { label: 'Agent 运行时', slug: 'docs/technical/agents' },
            { label: '取消、恢复与幂等', slug: 'docs/technical/recovery' },
            { label: '测试', slug: 'docs/technical/testing' },
          ],
        },
        {
          label: '参考',
          items: [
            { label: 'API', slug: 'docs/reference/api' },
            { label: '更新日志', slug: 'docs/reference/changelog' },
            { label: '许可证', slug: 'docs/reference/license' },
          ],
        },
        {
          label: '参与',
          items: [{ label: '贡献与反馈', slug: 'docs/contributing' }],
        },
      ],
      customCss: ['./src/styles/starlight.css'],
      head: [
        { tag: 'meta', attrs: { property: 'og:type', content: 'website' } },
        { tag: 'meta', attrs: { property: 'og:site_name', content: 'CongoRAG' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#0b0b0f' } },
      ],
    }),
  ],
});
