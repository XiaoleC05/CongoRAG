package ctxmgr

import (
	"context"

	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

// Compressor 是策略模式的落地（技术方案 §4.1）：压缩算法可替换。
//
// 【只压 Compressible 的条目】items 参数只会收到 Recent Messages 和
// Summary（Usecase.Build 保证），不会把 RAG Chunk 或 System Prompt
// 递给它压缩——那两类的处理方式是整条删除或永不删除,不是"压缩"。
type Compressor interface {
	Compress(ctx context.Context, items []Item, targetTokens int, tok llm.Tokenizer) ([]Item, error)
}

// Manager 是 Usecase 对外的接口——conversation 包依赖这个接口，
// 不依赖 *ctxmgr.Usecase 这个具体类型（规则 A）。
type Manager interface {
	Build(ctx context.Context, req Request) (*FinalContext, error)
}
