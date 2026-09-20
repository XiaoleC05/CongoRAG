// Package knowledge 拥有知识库和它的文档。
//
// 知识库、文档、上传写路径、文档处理管道、孤儿文件对账都在这个包里。
// 方案的目录树原本把 knowledge 和 document 分成两个包，合并的理由见代码架构设计 §0：
// 知识库和它的文档是一体的，合并后两者之间的循环依赖直接消失。
package knowledge

import (
	"time"

	"github.com/google/uuid"
)

// KB 是一个知识库，字段与 migrations/0001_init.up.sql 里的 knowledge_bases 表一一对应。
//
// 放在本包而不是 domain 包：domain 装的是跨模块共享的中性类型，
// KB 目前只有本包使用。等第二个包也需要它时再提上去。
type KB struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}
