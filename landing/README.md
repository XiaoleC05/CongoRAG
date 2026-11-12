# CongoRAG 公开首页

这是部署在 https://congorag.cloud/ 的静态首页，不依赖构建工具，也没有外部字体或 JS 依赖。

- `index.html`：页面、样式与交互全部内联；真实界面截图来自本仓库的演示库。
- `assets/`：压缩后的界面截图（1400px 宽，JPEG）与 favicon。
- 部署：把本目录内容放到 nginx 的 `/opt/congorag-demo/www/`，并在域名 server 中加

```nginx
location ^~ /landing/ {
    auth_basic off;
    alias /opt/congorag-demo/www/;
    expires 7d;
    add_header Cache-Control "public, max-age=604800";
}
```

首页本身由 `location = /` 返回 `index.html`；页面内图片用 `/landing/assets/...` 绝对路径。

设计说明：纸张版式、衬线标题、页边批注与 Figure 编号来自 `design-taste-frontend` skill 的
"从主题找构图"练习；首页只保留一个主要动作（打开演示），移动端每节先读结论再看截图。
