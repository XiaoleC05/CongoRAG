# 参赛版一键部署（含 PDF 解析 sidecar）

## 1. 构建镜像

在**仓库根目录**执行：

```bash
docker build --target api     -t congorag-api:cup .
docker build --target worker  -t congorag-worker:cup .
docker build --target migrate -t congorag-migrate:cup .
```

PDF sidecar 由 compose 的 `build:` 直接构建（首次约 3~5 分钟，主要是 pip 安装
onnxruntime/pymupdf）。

## 2. 起服务

```bash
cd deployments/competition
cp .env.example .env
docker compose up -d --build
```

`docker compose ps` 应看到 5 个容器：postgres / pdf-service / api / worker 在跑，
migrate 是 `Exited (0)`（一次性迁移容器，正常）。等 api 变成 `healthy` 后，
浏览器打开 <http://127.0.0.1:3210>。

## 3. 配置模型（BYOK）

界面引导页填：API 地址（如 `https://api.siliconflow.cn/v1`）、API Key、
chat 模型（建议 `deepseek-ai/DeepSeek-V3`，引用合规更好）、embedding 模型
（`BAAI/bge-m3`，1024 维）。Key 用主密钥加密存在数据库里，主密钥在数据卷
`/data/master.key`。

## 4. 验收清单

- 上传 `1706.03762v1.pdf` → 状态 ready，page_count=15，分块带页码
- 提问"训练用了多少块 GPU" → SSE citation 带 page；点引用 → 阅读器 bbox 高亮
- 阅读器：章节目录侧栏、整页翻译、参考文献（库内命中可点）
- 检索调试：融合/向量/词法三路分数
- Agent：arxiv_search 工具能返回真实论文

## 5. 公网 demo（可选）

默认只绑宿主机回环，外网访问不了。省赛评审需要公网链接时，最省事的是
在上面加一层 Caddy 做 HTTPS + Basic Auth：

```bash
docker run --rm caddy:2 caddy hash-password --plaintext '你的访问口令'
```

Caddyfile 示例：

```
demo.example.com {
    basic_auth {
        reviewer <bcrypt-hash>
    }
    reverse_proxy 127.0.0.1:3210
}
```

> 演示机注意：**不要把真实 API Key 交给评审**。BYOK 演示模式建议单独配一个
> 限额 key，并在评审结束后轮换。
