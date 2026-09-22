package retrieval

import (
	"context"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// Repo 是 document_chunks 的存取接口。
//
// 【InsertChunks 为什么多一个 vecs 参数，和方案原稿的签名不一样】
// 方案 §5.5 写的是 `InsertChunks(ctx, q, docID, chunks []domain.Chunk, model string)`，
// 隐含"chunks 里已经带着向量"。但 domain.Chunk 只有 Content/Score 两个
// 有意义的字段（model.go 的注释：它同时服务"索引输入"和"检索输出"两种
// 场景，检索结果不该意外携带一个几百维的向量数组一起被序列化）。
// 所以向量单独用一个平行数组传，索引长度必须和 chunks 对应——
// 这是 usecase.go 的 ReplaceChunks 内部保证的，Repo 只管接收。
type Repo interface {
	InsertChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk, vecs [][]float32, model string) error

	DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error

	// Search 做向量检索。vec 是查询文本已经 embed 好的向量,model 是算出
	// 这个向量的模型名——检索查询必须带 embedding_model 过滤（技术方案
	// §六：混维度列上不加过滤的向量运算会直接报 22000,不是变慢)。
	//
	// 返回的 domain.Chunk 会带 Filename（Chunk 类型注释：只有 Search
	// 的返回值会填这个字段），JOIN documents 表拿到，给 SSE 的 citation
	// 事件用。
	Search(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error)
}
