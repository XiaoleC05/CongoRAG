package platform

import "fmt"

// 列表分页的公共部分（issue #45）。
//
// knowledge / agent / conversation 三个上下文都要做同一件事：校验 limit、
// 解游标、拼下一页游标。各写一份必然会漂移——默认页大小在三个列表页里
// 应该是一样的，而"越界是报错还是夹取"这种判断更不能各写各的。

const (
	// MaxListLimit 是一页最多多少行。
	//
	// 【为什么是 200】它是"一页仍然是可接受的响应体"的判断。和
	// retrieval 的 embedBatchSize（256）无关，不要混。
	MaxListLimit = 200

	// DefaultListLimit 是调用方没给 limit 时的页大小。
	//
	// 【为什么是 50】conversation 的 recentMessagesLimit 已经是这个项目里
	// "一次给多少条"的既有取值，与它对齐能让"前端一屏看到的条数"在所有
	// 页面里一致。
	DefaultListLimit = 50
)

// ClampListLimit 把调用方给的 limit 收敛到合法区间。
//
// 【0 是"没给"，负数不是】0 表示调用方没有指定页大小，用默认值；而负数
// 是一个明确规定过的坏值，必须报错——把它也当成"没给"会让 `?limit=-5`
// 静默变成 50，客户端永远不知道自己的参数写错了。
//
// 【越界报错而不是静默夹取】夹取会让调用方以为 limit=1000 拿到了 1000 条，
// 实际拿到 200，而接口返回的是 200 状态码——"看起来成功但内容少了一半"
// 是最难排查的一类失败。
func ClampListLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 0 || limit > MaxListLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d, got %d: %w", MaxListLimit, limit, ErrInvalid)
	}
	return limit, nil
}

// ParseCursor 解一个从查询参数来的游标。
//
// 【空串表示"从第一页开始"，不是"一个坏游标"】两者混在一起会让第一页请求
// 变成 400。
func ParseCursor(raw string) (*ListCursor, error) {
	if raw == "" {
		return nil, nil
	}
	return DecodeCursor(raw)
}

// EncodeNextCursor 拼下一页的游标；没有下一页时返回空串。
//
// 【空串是"没有更多"的内部表达】契约里的 nextCursor 是 nullable 字符串，
// handler 会把空串转成 JSON null——前端拿到 null 就是"到底了"。
//
// sortKey / tiebreak 由调用方从最后一行取：只有它知道自己的排序键是什么
// （时间戳 + id、或者单独的 sequence_no）。
func EncodeNextCursor(hasMore bool, sortKey, tiebreak string) string {
	if !hasMore {
		return ""
	}
	return EncodeCursor(sortKey, tiebreak)
}
