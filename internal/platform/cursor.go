package platform

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// ListCursor 是一次分页查询的起点：上一页最后那一行的排序键。
//
// 【为什么是两个字段】三个要分页的端点按「时间 + id」排序（documents 与
// agent_runs 都是 created_at DESC, id DESC）。只带时间是不够的——同一毫秒
// 里创建的两行时间戳相同，只按时间翻页会把它们中的一个跳过或重复一次。
// 带上 id 作为并列时的第二排序键，`(created_at, id) < (cursorTime, cursorID)`
// 就是一个严格全序。
//
// 【messages 只用 SortKey】它按 sequence_no 排，而 sequence_no 在一个会话
// 内由 advisory lock 内分配，天然唯一，不需要第二排序键。所以 Tiebreak 允许
// 为空——编码格式对空值一样成立。
type ListCursor struct {
	SortKey  string
	Tiebreak string
}

// cursorVersion 是游标格式的版本前缀。
//
// 【为什么要有它】游标对客户端是不透明字符串，但格式将来可能要改（比如多带
// 一个排序维度）。没有版本前缀的话，一个旧格式的游标会被新代码按新格式解出
// 一个**错的**起点——那一页会静默地多几行或少几行。有了前缀，解不出来就是
// 400，客户端重新从第一页开始，行为是可预期的。
const cursorVersion = "v1"

// cursorSep 分隔排序键与并列键。
//
// 用一个不可能出现在排序键里的字符：排序键是 RFC3339 时间戳或十进制数字，
// 都不含 '|'。
const cursorSep = '|'

// EncodeCursor 把排序键编成一个不透明游标。
func EncodeCursor(sortKey, tiebreak string) string {
	raw := sortKey + string(cursorSep) + tiebreak
	return cursorVersion + ":" + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor 解一个游标。任何解不出来的情况都返回包了 ErrInvalid 的错误，
// 于是经 handler 变成 400 invalid_argument——客户端立刻知道自己的参数不对，
// 而不是拿到一页莫名其妙的数据。
//
// 【为什么所有失败都归成 ErrInvalid】空游标是合法的（表示从第一页开始），
// 调用方传空表示"从头开始"，不会走到这里；走到了就是客户端真给了一个值。
// 那个值解不出来只有一个原因：它不是我发的。
func DecodeCursor(s string) (*ListCursor, error) {
	prefix, payload, ok := strings.Cut(s, ":")
	if !ok || prefix != cursorVersion {
		return nil, fmt.Errorf("cursor has an unrecognised format: %w", ErrInvalid)
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("cursor is not valid base64: %w", ErrInvalid)
	}

	sortKey, tiebreak, ok := strings.Cut(string(raw), string(cursorSep))
	if !ok {
		return nil, fmt.Errorf("cursor is missing its separator: %w", ErrInvalid)
	}
	if sortKey == "" {
		return nil, fmt.Errorf("cursor has an empty sort key: %w", ErrInvalid)
	}

	return &ListCursor{SortKey: sortKey, Tiebreak: tiebreak}, nil
}
