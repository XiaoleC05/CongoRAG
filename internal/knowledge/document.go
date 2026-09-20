// document.go 放文档的领域类型和状态机——它和 model.go 里的 KB 分成两个文件，
// 只是因为一个文件放不下这么多东西，两者仍然是同一个包（§0：知识库和它的
// 文档是一体的）。
package knowledge

import (
	"time"

	"github.com/google/uuid"
)

// Status 是文档处理管道的状态。
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
)

// transitions 是状态迁移表：方案 §4.1 要求「非法迁移显式拒绝」。
//
// 【只有四态，没有 uploading】方案原稿的五态里有个 uploading，但写路径
// （Upload 方法）里没有任何一条边会产生它——rename 成功之前，document 这一行
// 根本不存在于数据库里；它是一个不可达状态，已经从方案里删掉。
//
// 【failed->queued 和 ready->processing 目前没有调用点】UpdateStatus 实际用到的
// 只有 queued->processing、processing->ready、processing->failed 三条边，全部在
// ProcessDocument 里。剩下两条是留给还没做的"重试 / 重建索引"入口的——声明在
// 先、调用点在后，但必须写清楚它们是空的，否则这张表看起来像已经实现了重试。
//
// 【重试不靠 failed->queued】一次可恢复的失败不会把文档落成 failed：不是最后
// 一次 attempt 就只把错误交回 River，状态留在 processing，下一次投递接着跑
// （理由见 ProcessDocument）。failed 因此等于"已放弃"，由最后一次 attempt 落下。
var transitions = map[Status][]Status{
	StatusQueued:     {StatusProcessing, StatusFailed},
	StatusProcessing: {StatusReady, StatusFailed, StatusQueued}, // 回到 queued = 允许重试
	StatusReady:      {StatusProcessing},                        // 重新索引（换 embedding 模型之类）
	StatusFailed:     {StatusQueued},                            // 重试
}

// CanTransition 判断从 s 状态迁到 to 状态是否合法。
func (s Status) CanTransition(to Status) bool {
	for _, ok := range transitions[s] {
		if ok == to {
			return true
		}
	}
	return false
}

// Document 是一份上传的文档，字段与 migrations/0001_init.up.sql 的 documents 表对应。
type Document struct {
	ID              uuid.UUID
	KnowledgeBaseID uuid.UUID
	Filename        string
	StorageKey      string
	Status          Status
	ByteSize        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
