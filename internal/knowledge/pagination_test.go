package knowledge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 列表分页（issue #45）
//
// 这条路径上最贵的缺陷都不报错：翻页漏一行、重复一行、或者把「加载更多」
// 永远亮着。所以测试钉的是"两页拼起来恰好等于全量"这类集合性质，
// 而不是单个返回值长什么样。
// ════════════════════════════════════════════════════════════════

// seedDocuments 造 n 份文档，created_at 逐秒递增（同一秒内的并列问题由
// 单独一条用例覆盖）。
func seedDocuments(d *testDeps, kbID uuid.UUID, n int) []*Document {
	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	docs := make([]*Document, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, &Document{
			ID: uuid.New(), KnowledgeBaseID: kbID, Filename: fmt.Sprintf("doc-%02d.md", i),
			Status: StatusReady, CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	d.docRepo.docs = docs
	return docs
}

// 【这是分页最重要的一条】把每一页接起来，必须恰好等于全量：不重、不漏、
// 顺序一致。任何一个"翻页漏一行"的实现都会在这里红。
func TestListDocuments_PagesConcatenateToTheFullSet(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()
	seedDocuments(d, kbID, 7)

	const pageSize = 3
	var got []*Document
	cursor := ""
	for i := 0; i < 10; i++ { // 10 是防死循环的上限，正常三轮就到底了
		page, next, err := d.uc.ListDocuments(context.Background(), kbID, cursor, pageSize)
		require.NoError(t, err, "第 %d 页", i)
		require.LessOrEqual(t, len(page), pageSize, "一页不能超过 limit")
		got = append(got, page...)
		if next == "" {
			break
		}
		cursor = next
	}

	require.Len(t, got, 7, "两页拼起来必须恰好是全量，不重不漏")

	seen := map[uuid.UUID]bool{}
	for i, doc := range got {
		assert.False(t, seen[doc.ID], "第 %d 条重复出现：%s", i, doc.ID)
		seen[doc.ID] = true
		if i > 0 {
			assert.True(t, got[i-1].CreatedAt.After(doc.CreatedAt),
				"必须严格按 created_at 倒序，第 %d 条与第 %d 条顺序不对", i-1, i)
		}
	}
}

// 同一毫秒创建的两行：只按时间排序会稳定地多出一条或漏掉一条，
// id 作为第二排序键让 (created_at, id) 成为严格全序。
func TestListDocuments_TiesOnCreatedAtAreNotLostOrDuplicated(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()

	same := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	d.docRepo.docs = []*Document{
		{ID: uuid.New(), KnowledgeBaseID: kbID, Status: StatusReady, CreatedAt: same},
		{ID: uuid.New(), KnowledgeBaseID: kbID, Status: StatusReady, CreatedAt: same},
		{ID: uuid.New(), KnowledgeBaseID: kbID, Status: StatusReady, CreatedAt: same},
	}

	var got []*Document
	cursor := ""
	for i := 0; i < 10; i++ {
		page, next, err := d.uc.ListDocuments(context.Background(), kbID, cursor, 1)
		require.NoError(t, err)
		got = append(got, page...)
		if next == "" {
			break
		}
		cursor = next
	}

	require.Len(t, got, 3, "时间戳并列时也必须不重不漏")
}

// 最后一页的 nextCursor 必须是空串（前端据此把「加载更多」收起来）。
func TestListDocuments_LastPageHasNoCursor(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()
	seedDocuments(d, kbID, 2)

	page, next, err := d.uc.ListDocuments(context.Background(), kbID, "", 50)

	require.NoError(t, err)
	assert.Len(t, page, 2)
	assert.Empty(t, next, "没有更多时必须给空游标，否则「加载更多」会一直亮着、点了没反应")
}

// 恰好整除时也不能多给一个空页——那会让用户点一次「加载更多」什么都没发生。
func TestListDocuments_ExactMultipleDoesNotYieldAnEmptyPage(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()
	seedDocuments(d, kbID, 4)

	page, next, err := d.uc.ListDocuments(context.Background(), kbID, "", 4)

	require.NoError(t, err)
	assert.Len(t, page, 4)
	assert.Empty(t, next, "刚好取满时不该再有下一页")
}

// limit 越界报错而不是静默夹取。
func TestListDocuments_RejectsOutOfRangeLimit(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()

	for _, limit := range []int{-1, platform.MaxListLimit + 1} {
		_, _, err := d.uc.ListDocuments(context.Background(), kbID, "", limit)
		assert.ErrorIs(t, err, platform.ErrInvalid, "limit=%d", limit)
	}
}

// limit 为 0 表示"没给"，用默认值——不是错。
func TestListDocuments_ZeroLimitMeansDefault(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()
	seedDocuments(d, kbID, 3)

	page, _, err := d.uc.ListDocuments(context.Background(), kbID, "", 0)

	require.NoError(t, err)
	assert.Len(t, page, 3)
}

// 坏游标必须是 400 那一档，且不能悄悄退回第一页——那会让用户看到重复的内容。
func TestListDocuments_RejectsMalformedCursor(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()

	for _, cursor := range []string{"!!!", "v2:AAAA", "v1:" + "bm90LWEtdGltZXN0YW1w"} {
		_, _, err := d.uc.ListDocuments(context.Background(), kbID, cursor, 10)
		assert.ErrorIs(t, err, platform.ErrInvalid, "cursor=%q", cursor)
	}
}
