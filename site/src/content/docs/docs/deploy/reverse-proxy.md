---
title: 反向代理与安全
description: 把 CongoRAG 放到远程机器上之前，必须先补的那一层。
---

CongoRAG 没有登录、没有权限系统、没有多租户。默认只监听 `127.0.0.1:3210`，这是它唯一的安全边界。

如果你要从另一台机器访问，先决定谁来挡在它前面。不要直接把 3210 端口映射到公网。

## 最低要求

- 只开 HTTPS。
- 在反向代理层加认证：Basic Auth、OIDC、VPN、Tailscale 都行。
- 限制来源 IP，或者只允许内网访问。
- 不要把 API 路径绕开认证单独放出去。

Basic Auth 是一个共享密码，不是多用户系统。它能挡住陌生人，但不能区分“谁改了什么”。多人使用需要更强的方案。

## Nginx 示例

假设 api 跑在 `127.0.0.1:3210`，并且已经生成 `/etc/nginx/.htpasswd`：

```nginx
server {
    listen 443 ssl http2;
    server_name rag.example.com;

    ssl_certificate     /etc/letsencrypt/live/rag.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/rag.example.com/privkey.pem;

    auth_basic           "CongoRAG";
    auth_basic_user_file /etc/nginx/.htpasswd;

    client_max_body_size 32m;

    location / {
        proxy_pass http://127.0.0.1:3210;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;

        # SSE：关掉缓冲，否则 token 会被攒着一起发
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
```

几个点：

- `proxy_buffering off` 是给 SSE 用的。不关的话，回答可能看起来“卡住”，最后一次性出现。
- `client_max_body_size` 要不小于 `CONGORAG_MAX_UPLOAD_BYTES`，否则上传会先被 Nginx 拦掉。
- 不需要 WebSocket。CongoRAG 的流是 HTTP 上的 SSE。
- 不需要给应用传 CORS。前端和 API 同源。

## 如果 api 在 Docker 里

compose 默认把宿主机端口绑在 `127.0.0.1`，Nginx 直接代理这个端口即可。不要把 compose 的 ports 改成 `3210:3210`，那会绕过 Nginx 的认证。

## 不要做的事

- 不要用 `proxy_pass` 把整个站点裸奔到公网，然后指望“没人知道地址”。
- 不要在 URL 里放 API Key 或长期令牌。
- 不要把 PostgreSQL 的 5432 端口也映射出去。它只需要给 api/worker 用。
