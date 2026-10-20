# congorag-daily

每天抓 GitHub Trending，用 DeepSeek 的 Anthropic 兼容接口写摘要，渲染成一份邮件日报，发到指定邮箱。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `DEEPSEEK_API_KEY` | 必填。DeepSeek 的 API Key |
| `DEEPSEEK_BASE_URL` | 默认 `https://api.deepseek.com/anthropic` |
| `DEEPSEEK_MODEL` | 默认 `deepseek-flash` |
| `SMTP_HOST` / `SMTP_PORT` | 默认 `smtp.qq.com:465` |
| `SMTP_USER` / `SMTP_PASS` | QQ 邮箱账号和授权码 |
| `MAIL_TO` | 收件地址 |
| `MAIL_FROM_NAME` | 发件人显示名，默认 `CONGORAG DAILY` |
| `DAILY_SITE_URL` | 默认 `https://oxelia51.com` |
| `DAILY_STATE_DIR` | 记录最后发送日期和期号 |
| `DAILY_WEB_DIR` | 网页版存档目录；不设就不写 |

## 用法

```bash
go build -o congorag-daily .

# 用 fixture 渲染，不发信
./congorag-daily -fixture fixture.json -out out

# 从 fixture 发一封测试信
./congorag-daily -fixture fixture.json -send

# 正常跑：抓当日 trending、调模型、发信
./congorag-daily

# 同一天强制重发
./congorag-daily -force
```

## 请求格式

模型调用走 Anthropic Messages 格式：

- 路径：`{DEEPSEEK_BASE_URL}/v1/messages`
- 请求头：`x-api-key`、`anthropic-version: 2023-06-01`
- 请求体：`model`、`max_tokens`、`system`、`messages`

不依赖 OpenAI SDK。返回的 `content[].text` 里是 JSON 摘要，解析失败会退回各仓库的原始描述。

## 事实约束

- 只把 GitHub Trending 页面和仓库 README 放进 prompt。
- prompt 明确禁止补充未给出的功能、数字、融资、团队和用户数。
- 不写“为什么上榜”的因果。
- 页脚注明事实来源、生成方式，并提示以仓库原文为准。

## 部署

服务器上只需要二进制、`/etc/congorag/daily.env` 和 systemd timer。不要在服务器上编译。
