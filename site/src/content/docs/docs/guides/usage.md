---
title: 用量
description: 每次模型调用记了什么，以及怎么读汇总。
---

CongoRAG 记录 token 用量，不计算金额。你用的是自己的模型账号，价格和账单在服务商那边。

## 记什么

每次 chat 或 embedding 调用记一行：

- 模型 id 和模型名
- 类型：chat / embedding
- prompt tokens
- completion tokens
- 调用次数

上游没有返回 usage 时，调用次数仍然加一，token 记 0。这样至少能看出“调用发生了”，不会因为上游少一个字段就整条记录消失。

## 怎么读

`GET /api/v1/usage` 返回按模型聚合的结果：

```json
{
  "byModel": [
    {
      "modelId": "uuid",
      "modelName": "deepseek-chat",
      "kind": "chat",
      "calls": 12,
      "promptTokens": 34567,
      "completionTokens": 8901
    }
  ],
  "totalPromptTokens": 34567,
  "totalCompletionTokens": 8901
}
```

字段以契约 `contracts/openapi.yaml` 为准。这个接口只读，不会改数据。

## 看不到金额

价格表不在本地维护。不同服务商、不同模型、不同计费方式（按 token、按次、缓存命中）都不一样，写死一个价格只会误导。

要算钱，把 token 数拿去乘服务商的单价。

## 用量会一直增长吗

会。每次调用一行，没有自动清理。长期使用要自己定期归档或删除旧行；删除模型或会话时，相关行会按外键级联处理，不会留下孤儿。
