---
title: 配置
description: CongoRAG 的环境变量、默认值和主密钥。
---

CongoRAG 没有配置文件，全部通过环境变量传入。只有 `CONGORAG_DB_URL` 是必填的。

## 变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CONGORAG_DB_URL` | 无，必填 | PostgreSQL 连接串 |
| `CONGORAG_LISTEN_ADDR` | `127.0.0.1:3210` | api 监听地址。默认只绑回环 |
| `CONGORAG_DOCUMENTS_DIR` | `./data/documents` | 上传文件的存储根目录 |
| `CONGORAG_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `CONGORAG_MASTER_KEY` | 空 | 主密钥的十六进制串；为空时去密钥文件读 |
| `CONGORAG_MASTER_KEY_PATH` | `./data/master.key` | 主密钥文件路径 |
| `CONGORAG_TIKTOKEN_CACHE_DIR` | `./data/tiktoken-cache` | tiktoken 词表缓存目录 |
| `CONGORAG_MAX_UPLOAD_BYTES` | `33554432`（32 MiB） | 单次上传的请求体上限；0 或负数会退回默认值 |

`make dev` 会通过 make 的 `export` 注入 `CONGORAG_DB_URL`，所以在 cmd.exe 和 bash 里都能跑。直接运行二进制时，需要自己把变量放进环境。

## 主密钥

主密钥用来加密模型服务的 API Key。解析顺序：

1. `CONGORAG_MASTER_KEY` 非空，直接用。
2. 否则读 `CONGORAG_MASTER_KEY_PATH` 指向的文件。
3. 文件不存在，随机生成一个写入该路径，权限 `0600`。

主密钥只生成一次。它丢了，数据库里所有 provider 的 API Key 都解不开，只能重新填一遍。备份时把它和数据库一起备。

## 监听地址

默认 `127.0.0.1:3210` 是安全选择：不经过额外配置，局域网上的其他机器连不进来。

Docker 交付形态里，compose 会把容器内的监听地址覆盖成 `0.0.0.0:3210`，否则宿主机的端口映射转发不进去；真正的暴露面由 ports 的 `127.0.0.1:` 前缀控制。

如果你要把 api 放到公网，先读[反向代理与安全](/docs/deploy/reverse-proxy/)。CongoRAG 没有登录和权限系统，直接把端口暴露出去，等于把全部数据交给任何能访问它的人。

## 上传与文档

- 单次请求体上限 32 MiB，可以用 `CONGORAG_MAX_UPLOAD_BYTES` 调整。
- 支持的文件类型是 `.md`、`.markdown`、`.txt`，大小写不敏感。
- 原始文件存在 `CONGORAG_DOCUMENTS_DIR`，数据库只存 `storage_key` 和元数据。
- tiktoken 缓存只需要成功下载一次；之后可以离线启动。

## 日志

`CONGORAG_LOG_LEVEL` 默认 `info`。排查文档处理问题先看 worker 的日志；api 的日志不会替 worker 报错。

改日志级别不需要改代码，重启进程即可。
