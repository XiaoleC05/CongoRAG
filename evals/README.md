# evals/

RAG 质量测量。这里目前只有一样东西：`measure_recall.sh`——检索层的
ANN recall@K 测量（技术方案 §5.7）。

**这不是 M5 的那个 eval 数据集。** M5 要建的是"问题 / 标准答案 / 期望命中
的 chunk_id"三元组集合,测的是"答案对不对"；这里测的是"HNSW 近似索引
相对精确扫描漏掉了多少"，只关心检索这一层,不关心生成的回答。两者的
分母不是一回事,不要混着算。

## 怎么跑

```bash
export CONGORAG_DB_URL=postgres://postgres:postgres@127.0.0.1:5432/congorag?sslmode=disable
export CONGORAG_EMBED_BASE_URL=https://api.siliconflow.cn/v1   # 你自己配置的 provider
export CONGORAG_EMBED_API_KEY=sk-...
export CONGORAG_EMBED_MODEL=BAAI/bge-m3                        # 必须和当前生效的 embedding 模型一致

./evals/measure_recall.sh "查询文本"
```

产出一张三行小表：K 取 5/10/20,每行是 recall@K + 两种扫描方式各自的耗时。

## 2026-09-20 的一次真实测量

用 M2 验证阶段建的语料库（4 篇短文档、共 4 个分块：ConGoRAG 简介、
PostgreSQL 与向量检索、Go 并发模型、大语言模型上下文窗口）跑的,
查询是"HNSW 索引和向量检索的原理"：

| K | recall@K | 精确扫描耗时 | ANN 耗时 |
| --- | --- | --- | --- |
| 5 | 100.0% | 221ms | 232ms |
| 10 | 100.0% | 221ms | 220ms |
| 20 | 100.0% | 224ms | 217ms |

**这个 100% 不说明索引质量好,只说明语料太小。** 4 个分块时,
任何 K≥4 的查询——不管精确扫描还是 HNSW——都会把全部 4 条返回,
交集天然等于全集,recall 恒为 100%。这张表真正验证的是：

1. 精确扫描和 ANN 扫描这两条查询路径本身都跑通了（`SET
   enable_indexscan = off` 真的能让 planner 放弃索引，`SET
   hnsw.ef_search` 真的在生效）
2. 检索结果在语义上确实相关——查询"HNSW 索引和向量检索的原理"时,
   排第一的分块来自专门讲 PostgreSQL 和向量检索的那篇文档,不是随机
   哪一篇,说明 embedding 和检索链路的正确性,不是巧合命中
3. 测量脚本本身的方法论是对的,语料规模上来之后（M5 的 eval 数据集,
   或者用户实际积累的知识库）,同一个脚本能量出有意义的数字

**不要在语料这么小的时候得出"HNSW 在这个项目里没有损失"的结论**——
那需要几百到几千个分块、并且 top-K 明显小于总分块数时才有意义。
