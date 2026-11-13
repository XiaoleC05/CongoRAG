package platform

import (
	"testing"
)

// BenchmarkCursorRoundTrip 是分页游标的编解码：列表接口每页都要做一次，
// 且游标是 base64 编码的（分配明显），值得有一个固定基线。
func BenchmarkCursorRoundTrip(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		enc := EncodeCursor("2026-11-14T02:51:19.123456Z", "0f3c1f5e-9a2b-4c7d-8e6f-112233445566")
		got, err := DecodeCursor(enc)
		if err != nil || got == nil {
			b.Fatalf("round trip failed: %v", err)
		}
	}
}
