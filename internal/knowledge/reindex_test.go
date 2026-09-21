package knowledge

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 重新索引（issue #39）
//
// 在 #39 之前，"换 embedding 模型"只能得到 409，应用内没有任何清空或重建
// 入口——用户唯一的办法是手工连库删向量。这一组钉的是恢复路径本身。
// ════════════════════════════════════════════════════════════════

// addKB 往假 KB repo 里塞一个知识库，供"整库重建"的用例当目标。
func addKB(t *testing.T, d *testDeps) uuid.UUID {
	t.Helper()
	kb := &KB{ID: uuid.New(), Name: "测试库"}
	require.NoError(t, d.kbRepo.Insert(context.Background(), nil, kb))
	return kb.ID
}

// 单文档重建：状态被 CAS 回 queued，且任务恰好排了一次。
func TestReindexDocument_ReadyBecomesQueuedAndEnqueues(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{{ID: docID, Status: StatusReady}}

	require.NoError(t, d.uc.ReindexDocument(context.Background(), docID))

	assert.Equal(t, StatusQueued, d.docRepo.docs[0].Status)
	assert.Equal(t, []uuid.UUID{docID}, d.enq.enqueued)
}

// 已经在排队里的文档再点一次重建没有意义——返回冲突而不是静默成功，
// 用户才知道"这次没排新的"。
func TestReindexDocument_AlreadyQueuedReturnsConflict(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{{ID: docID, Status: StatusQueued}}

	err := d.uc.ReindexDocument(context.Background(), docID)

	require.ErrorIs(t, err, platform.ErrConflict)
	assert.Empty(t, d.enq.enqueued, "冲突时不该入队")
}

// 入队失败必须把错误交出来，且状态不能停在"已经标成 queued 但没人处理"。
//
// 【这条钉的是事务性】真实实现里状态标记与入队在同一个事务里，入队报错
// 会让整个事务回滚。假实现不模拟回滚（见 fakeTxManager 的注释），所以这里
// 只钉"错误传上来了"——那是回滚能发生的前提。
func TestReindexDocument_EnqueueFailurePropagates(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{{ID: docID, Status: StatusReady}}
	d.enq.fail = true

	err := d.uc.ReindexDocument(context.Background(), docID)

	require.Error(t, err)
	assert.Empty(t, d.enq.enqueued)
}

// 整库重建要把库里所有「可重建」的文档都排上，包括正在 processing 的那一份。
//
// 【为什么包含 processing 是刻意的】跳过它，它的旧模型向量会在切换事务里
// 被漏掉（READ COMMITTED 下看不见它之后提交的行），结果是永久停在
// 「ready + 旧向量 + 检索查不到」。见 document.go 的 transitions 注释。
func TestReindexKnowledgeBase_EnqueuesEveryRebuildableDocument(t *testing.T) {
	d := newFullTestUsecase()
	kbID := addKB(t, d)
	otherKB := uuid.New()

	ready := uuid.New()
	failed := uuid.New()
	processing := uuid.New()
	alreadyQueued := uuid.New()
	otherKBDoc := uuid.New()

	d.docRepo.docs = []*Document{
		{ID: ready, KnowledgeBaseID: kbID, Status: StatusReady},
		{ID: failed, KnowledgeBaseID: kbID, Status: StatusFailed},
		{ID: processing, KnowledgeBaseID: kbID, Status: StatusProcessing},
		{ID: alreadyQueued, KnowledgeBaseID: kbID, Status: StatusQueued},
		{ID: otherKBDoc, KnowledgeBaseID: otherKB, Status: StatusReady},
	}

	n, err := d.uc.ReindexKnowledgeBase(context.Background(), kbID)

	require.NoError(t, err)
	assert.Equal(t, 3, n, "ready / failed / processing 三种都该重排，queued 的不算")
	assert.ElementsMatch(t, []uuid.UUID{ready, failed, processing}, d.enq.enqueued)
	assert.NotContains(t, d.enq.enqueued, otherKBDoc, "别的知识库的文档不该被牵连")
}

// 知识库不存在时必须是 404，而不是"0 份文档，看起来成功了"。
func TestReindexKnowledgeBase_UnknownKBReturnsNotFound(t *testing.T) {
	d := newFullTestUsecase()

	n, err := d.uc.ReindexKnowledgeBase(context.Background(), uuid.New())

	require.ErrorIs(t, err, platform.ErrNotFound)
	assert.Zero(t, n)
}

// 空库是合法状态：排 0 份，不报错，也不该入队任何东西。
func TestReindexKnowledgeBase_EmptyKBEnqueuesNothing(t *testing.T) {
	d := newFullTestUsecase()
	kbID := addKB(t, d)

	n, err := d.uc.ReindexKnowledgeBase(context.Background(), kbID)

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, d.enq.enqueued)
}

// RequeueAllDocuments 是 llm 换模型时走的那条路：不区分知识库，全库重排。
// 它不自己开事务（事务边界由调用方持有，见 port.go 的论证）。
func TestRequeueAllDocuments_MarksAndEnqueuesEverything(t *testing.T) {
	d := newFullTestUsecase()
	kbA, kbB := uuid.New(), uuid.New()
	a, b := uuid.New(), uuid.New()

	d.docRepo.docs = []*Document{
		{ID: a, KnowledgeBaseID: kbA, Status: StatusReady},
		{ID: b, KnowledgeBaseID: kbB, Status: StatusProcessing},
	}

	n, err := d.uc.RequeueAllDocuments(context.Background(), nil)

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.ElementsMatch(t, []uuid.UUID{a, b}, d.enq.enqueued)
	assert.Equal(t, StatusQueued, d.docRepo.docs[0].Status)
	assert.Equal(t, StatusQueued, d.docRepo.docs[1].Status)
}

// 一份可重建的文档都没有时（新库、或者全都还在 queued）应当是 0 而不是错误。
func TestRequeueAllDocuments_NoRebuildableDocumentsIsNotAnError(t *testing.T) {
	d := newFullTestUsecase()

	n, err := d.uc.RequeueAllDocuments(context.Background(), nil)

	require.NoError(t, err)
	assert.Zero(t, n)
}
