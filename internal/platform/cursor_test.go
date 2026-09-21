package platform

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分页游标的编解码（issue #45）。
//
// 它对客户端是不透明字符串，所以"解不出来"是唯一允许的失败方式——解码器
// 绝不能猜。一页数据少几行或多几行都不会报错，只会让用户看到重复或缺失的
// 记录，那是最难归因的一类缺陷。

func TestCursor_RoundTrip(t *testing.T) {
	for _, tc := range []struct{ sortKey, tiebreak string }{
		{"2026-09-21T10:00:00Z", "3f2b1c4d-0000-0000-0000-000000000001"},
		{"2026-09-21T10:00:00.123456789Z", "00000000-0000-0000-0000-000000000000"},
		// messages 那条路径只用排序键，并列键为空。
		{"42", ""},
	} {
		got, err := DecodeCursor(EncodeCursor(tc.sortKey, tc.tiebreak))

		require.NoError(t, err)
		assert.Equal(t, tc.sortKey, got.SortKey)
		assert.Equal(t, tc.tiebreak, got.Tiebreak)
	}
}

// 【所有解不出来的情况都必须是 ErrInvalid】这样 handler 会把它映射成
// 400 invalid_argument，客户端立刻知道参数不对，而不是拿到一页错数据。
func TestCursor_MalformedInputsAreInvalidArgument(t *testing.T) {
	for _, tc := range []struct{ name, cursor string }{
		{"没有版本前缀", "MjAyNi0wOS0yMXwy"},
		{"版本前缀不认识", "v2:MjAyNi0wOS0yMXwy"},
		{"base64 解不开", "v1:!!!not-base64!!!"},
		{"缺少分隔符", "v1:" + b64("2026-09-21T10:00:00Z")},
		{"排序键为空", "v1:" + b64("|abc")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeCursor(tc.cursor)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}
}

// 空串不是"格式坏的游标"——调用方必须自己用"传没传"来判断，走到解码器
// 的空串一律按坏值处理（否则一个空游标会被当成合法的第一页起点，
// 掩盖调用方的逻辑错误）。
func TestCursor_EmptyStringIsRejected(t *testing.T) {
	_, err := DecodeCursor("")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalid)
}

// 排序键里含分隔符是编码器不该产生、解码器也不该接受的情况——
// 用 Cut（只切第一个）意味着后面的部分会原样留在并列键里，不会崩。
func TestCursor_SeparatorInsideSortKeyDoesNotPanic(t *testing.T) {
	got, err := DecodeCursor(EncodeCursor("a|b", "c"))

	require.NoError(t, err)
	assert.Equal(t, "a", got.SortKey)
	assert.Equal(t, "b|c", got.Tiebreak,
		"Cut 只切第一个分隔符，多出来的部分留在并列键里——排序键是时间戳或数字，不会出现这种情况")
}

// b64 只做 base64 那一层，让上面那几个用例能精确表达"坏在哪一步"。
func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
