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
// 【ready->queued 与 failed->queued 现在有调用点了（issue #39）】"重新索引"
// 入口走的是这两条边：入队侧把文档直接 CAS 回 queued，再由 ProcessDocument
// 从 queued 推进到 processing。
//
// 【为什么入队侧写 queued 而不是 processing】queued = 排队等着 worker、
// processing = worker 正在它上面。队列里排着 500 份文档时同时显示 500 份
// "处理中"是另一种说谎——Upload 写 queued、ProcessDocument 写 processing
// 这条分工是已经建立的约定。
//
// 【processing->queued 是批量重建的取舍】批量重建会连正在 processing 的
// 一起重排（`MarkAllForReindex` 的 WHERE 里有它）。不排的话，那份文档的
// 向量是用旧模型算的，而切换事务的清空语句在 READ COMMITTED 下看不见它
// 之后才提交的行——它会永久停在「ready + 旧模型向量 + 检索查不到」。
// 代价只是一次可自愈的竞争：正在跑的任务最后那步 CAS 会失败，任务失败后
// 被 River 重投，读到 queued 就正常重处理。
//
// 【重试不靠 failed->queued】一次可恢复的失败不会把文档落成 failed：不是最后
// 一次 attempt 就只把错误交回 River，状态留在 processing，下一次投递接着跑
// （理由见 ProcessDocument）。failed 因此等于"已放弃"，由最后一次 attempt 落下。
// 那条边留给的是「用户显式要求重跑一份已经放弃的文档」。
var transitions = map[Status][]Status{
	StatusQueued:     {StatusProcessing, StatusFailed},
	StatusProcessing: {StatusReady, StatusFailed, StatusQueued}, // 回到 queued = 允许重试
	StatusReady:      {StatusProcessing, StatusQueued},          // 重新索引（换 embedding 模型之类）
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
	// ChunkCount 是这份文档当前的向量分块数（issue #82）。
	//
	// 【它不是 documents 表的一列】它是 document_chunks 的聚合结果，
	// 由读路径上的 LEFT JOIN 算出来（见 document_postgres.go 的
	// documentsSelectCols）。冗余成一列的话就得在切分、重建、删除三条
	// 路径上各自维护它，而其中任一条漏了都不会报错——数出来的数字和
	// 库里的分块对不上，正是最容易被忽略的那种不一致。
	//
	// 未处理完的文档是 0，不是"未知"。
	ChunkCount int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
