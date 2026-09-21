# ADR-004：向量列用 halfvec，不用 vector

- 状态：已采纳
- 日期：2026-09-21
- 相关：`migrations/0001_init.up.sql`、`internal/llm/postgres.go`、
  `internal/llm/model.go`、`internal/conversation/memory_postgres.go`

## 背景

pgvector 提供两种向量列类型，我们选了 `halfvec`（半精度）。这个决定此前
**只写在代码注释和一份未跟踪的设计文档里**——也就是说，仓库的读者能看到
「用了 halfvec」，看不到「为什么」。把理由搬进版本控制是这篇 ADR 的主要目的。

相关的三处事实：

- `migrations/0001_init.up.sql` 里两张表的 `embedding` 列都是无维度的
  `halfvec`，维度在引导页探测出来之后由 `ALTER TABLE ... TYPE halfvec(N)` 补上。
- `internal/llm/model.go` 的 `maxEmbeddingDim = 4000` 是引导页校验维度的上限。
- 检索 SQL 用 `<=>`（余弦距离）配 `halfvec_cosine_ops` 索引
  （`internal/retrieval/postgres.go`、`internal/conversation/memory_postgres.go`）。

## 决策

**向量列一律用 `halfvec`，HNSW 索引的 opclass 一律用 `halfvec_cosine_ops`。**

## 理由

1. **HNSW 索引的维度上限**：pgvector 的 HNSW 对 `vector` 的上限是 2000 维，
   对 `halfvec` 是 4000 维。**3072 维的 embedding 模型用 `vector` 根本建不出
   HNSW 索引**——这不是性能取舍，是能不能建的问题。而 BYOK 的产品定位意味着
   用户可能接入任意维度的模型。
2. **存储省一半**：halfvec 每维 2 字节，vector 每维 4 字节。
3. **精度损失可忽略**：半精度带来的距离误差量级是 1e-6，而检索要的是
   相对排序，不是绝对距离。

## 后果

- **好处**：3072 维这类主流大模型的 embedding 可以直接用；索引和存储都省。
- **代价**：写向量时要显式转成 halfvec——`InsertChunks` 用
  `pgvector.NewHalfVector`，检索参数写 `$n::halfvec`。**漏掉转换是一个
  不会静默出错的错误**（类型不匹配会直接报 22000），所以这一条风险可控。
- **必须一起记住的**：opclass 写成 `vector_cosine_ops` 会直接建不起来
  （opclass 不接受 halfvec）；而写成 `halfvec_l2_ops` 配 `<=>` 才是最危险的
  ——不报错，但 planner 会静默放弃索引退化成顺序扫描。
- **如果将来要重新考虑**：出现 4000 维以上的 embedding 模型时，`halfvec`
  也建不出 HNSW 索引，那时要讨论降维（Matryoshka 截断或另训一个投影），
  而不是换回 `vector`。
