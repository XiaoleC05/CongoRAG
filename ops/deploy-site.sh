#!/usr/bin/env bash
set -euo pipefail

# 部署 CongoRAG 静态站和日报二进制。
# 用法：bash deploy-site.sh <tag> [--switch]
# 不带 --switch 时只落文件、装 unit，不碰 Nginx。

TAG="$1"
if [ -z "$TAG" ]; then
  echo "usage: $0 <tag> [--switch]" >&2
  exit 2
fi
SWITCH=0
if [ "$2" = "--switch" ]; then SWITCH=1; fi

BASE="https://github.com/XiaoleC05/CongoRAG/releases/download/$TAG"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "== download $TAG =="
curl -fL --retry 3 --connect-timeout 15 -o "$TMP/site.tar.gz" "$BASE/site.tar.gz"
curl -fL --retry 3 --connect-timeout 15 -o "$TMP/congorag-daily-linux-amd64" "$BASE/congorag-daily-linux-amd64"
curl -fL --retry 3 --connect-timeout 15 -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"

cd "$TMP"
sha256sum -c SHA256SUMS

echo "== install files =="
install -d /var/www/congorag/releases/"$TAG"
tar -xzf site.tar.gz -C /var/www/congorag/releases/"$TAG"
ln -sfn /var/www/congorag/releases/"$TAG" /var/www/congorag/current

install -d /opt/congorag/bin /etc/congorag
install -m 755 congorag-daily-linux-amd64 /opt/congorag/bin/congorag-daily

if ! id -u congorag >/dev/null 2>&1; then
  useradd --system --home /var/lib/congorag-daily --shell /usr/sbin/nologin congorag
fi
install -d -o congorag -g congorag /var/lib/congorag-daily
install -d -o congorag -g congorag /var/lib/congorag-daily/state
install -d -o congorag -g congorag /var/lib/congorag-daily/public

echo "== systemd =="
cat > /etc/systemd/system/congorag-daily.service <<'UNIT'
[Unit]
Description=CongoRAG Daily digest
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=congorag
Group=congorag
EnvironmentFile=/etc/congorag/daily.env
WorkingDirectory=/var/lib/congorag-daily
ExecStart=/opt/congorag/bin/congorag-daily
StateDirectory=congorag-daily
CacheDirectory=congorag-daily
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
ReadWritePaths=/var/lib/congorag-daily
MemoryMax=256M
TimeoutStartSec=10min
UNIT

cat > /etc/systemd/system/congorag-daily.timer <<'UNIT'
[Unit]
Description=CongoRAG Daily digest timer

[Timer]
OnCalendar=*-*-* 09:00:00 Asia/Shanghai
Persistent=true
RandomizedDelaySec=300
Unit=congorag-daily.service

[Install]
WantedBy=timers.target
UNIT

systemctl daemon-reload
systemctl enable congorag-daily.timer

cat > /etc/nginx/sites-available/oxelia51.com.new <<'NGINX'
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    return 444;
}

server {
    listen 443 ssl http2 default_server;
    listen [::]:443 ssl http2 default_server;
    server_name _;
    ssl_certificate /etc/letsencrypt/live/oxelia51.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/oxelia51.com/privkey.pem;
    return 444;
}

server {
    listen 80;
    listen [::]:80;
    server_name oxelia51.com www.oxelia51.com;
    return 301 https://oxelia51.com$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name oxelia51.com;

    ssl_certificate /etc/letsencrypt/live/oxelia51.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/oxelia51.com/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;

    root /var/www/congorag/current;
    index index.html;

    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;

    error_page 404 /404.html;

    location = /download { return 301 /docs/quickstart/; }
    location = /changelog { return 301 /docs/reference/changelog/; }
    location = /community { return 301 https://github.com/XiaoleC05/CongoRAG/issues; }
    location ^~ /app/ { return 410; }
    location ^~ /auth/ { return 410; }
    location ^~ /api/ { return 410; }
    location ^~ /token/ { return 410; }
    location = /webhook { return 410; }

    location /daily/ {
        alias /var/lib/congorag-daily/public/;
        index index.html;
    }

    location /_astro/ {
        expires 1y;
        add_header Cache-Control "public, immutable";
        try_files $uri =404;
    }

    location / {
        try_files $uri $uri/ =404;
    }
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name www.oxelia51.com;

    ssl_certificate /etc/letsencrypt/live/oxelia51.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/oxelia51.com/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;

    return 301 https://oxelia51.com$request_uri;
}
NGINX

if [ "$SWITCH" = "1" ]; then
  echo "== switch nginx =="
  if [ -f /etc/nginx/sites-available/oxelia51.com ]; then
    cp -a /etc/nginx/sites-available/oxelia51.com "/etc/nginx/sites-available/oxelia51.com.bak.$(date +%s)"
  fi
  mv /etc/nginx/sites-available/oxelia51.com.new /etc/nginx/sites-available/oxelia51.com
  rm -f /etc/nginx/sites-enabled/default-ip /etc/nginx/sites-enabled/default-ip-ssl /etc/nginx/sites-enabled/stats.oxelia51.com
  ln -sfn /etc/nginx/sites-available/oxelia51.com /etc/nginx/sites-enabled/oxelia51.com
  nginx -t
  systemctl reload nginx
  echo "switched. check: curl -I https://oxelia51.com/"
else
  echo "files installed; nginx not switched."
  echo "preview: python3 -m http.server 8081 --directory /var/www/congorag/current"
fi

echo "daily env: /etc/congorag/daily.env (not created by this script)"
echo "after env is ready: systemctl start congorag-daily.service"
