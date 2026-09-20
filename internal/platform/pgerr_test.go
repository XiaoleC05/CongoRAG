package platform

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WrapPgErr 是"写错了不报错"的典型：映射错了不会编译失败、不会 panic，
// 只是 HTTP 状态码变成 500——而正确答案本该是 409 或 404。
// 所以它必须有测试兜住。

func TestWrapPgErr_UniqueViolationBecomesDuplicateKey(t *testing.T) {
	in := &pgconn.PgError{Code: "23505", ConstraintName: "knowledge_bases_name_key"}

	out := WrapPgErr(in)

	assert.ErrorIs(t, out, ErrDuplicateKey)
	// 约束名要留在文案里——排查时靠它定位是哪个唯一约束撞了
	assert.Contains(t, out.Error(), "knowledge_bases_name_key")
}

// 23505 按约束名分流：幂等键的重复【不是错误】，
// usecase 捕获 ErrIdempotentHit 后改为"返回已创建的资源"。
// 分流错了的后果：用户重复提交会看到 409 报错，而不是既有结果。
func TestWrapPgErr_IdempotencyKeyIsNotDuplicateKey(t *testing.T) {
	in := &pgconn.PgError{Code: "23505", ConstraintName: constraintIdempotencyKey}

	out := WrapPgErr(in)

	assert.ErrorIs(t, out, ErrIdempotentHit)
	assert.NotErrorIs(t, out, ErrDuplicateKey, "幂等命中不该被当成冲突")
}

func TestWrapPgErr_ForeignKeyViolation(t *testing.T) {
	in := &pgconn.PgError{Code: "23503", ConstraintName: "documents_knowledge_base_id_fkey"}

	out := WrapPgErr(in)

	assert.ErrorIs(t, out, ErrForeignKey)
	assert.Contains(t, out.Error(), "documents_knowledge_base_id_fkey")
}

// 23502（not_null_violation）同样是"请求数据碰了约束"——比如契约里的可选
// 字段被省略、代码把 nil slice 绑进了 NOT NULL 列（issue #18）。归成
// invalid_argument 才能让客户端看出是自己这一侧的输入问题，否则它落到
// classify() 的 default，变成一个问不出字段名的通用 500。
func TestWrapPgErr_NotNullViolationBecomesInvalidArgument(t *testing.T) {
	in := &pgconn.PgError{Code: "23502", ColumnName: "tool_names"}

	out := WrapPgErr(in)

	assert.ErrorIs(t, out, ErrInvalid)
	assert.Contains(t, out.Error(), "tool_names")
}

// 认不出的 SQLSTATE 必须【原样返回】。
//
// 包成某个 sentinel 的话，一个本该是 500 的数据库故障会被映射成 4xx，
// 客户端以为是自己的参数问题，而真正的故障没人发现。
func TestWrapPgErr_UnknownCodePassesThrough(t *testing.T) {
	in := &pgconn.PgError{Code: "40001", Message: "serialization failure"}

	out := WrapPgErr(in)

	assert.Same(t, in, out, "认不出的错误要原样返回，不要包")
	for _, sentinel := range []error{
		ErrNotFound, ErrConflict, ErrInvalid,
		ErrDuplicateKey, ErrForeignKey, ErrUpstream, ErrIdempotentHit,
	} {
		assert.NotErrorIs(t, out, sentinel)
	}
}

// 不是 PgError 的错误（网络断了、context 取消）也要原样返回。
func TestWrapPgErr_NonPgErrorPassesThrough(t *testing.T) {
	in := errors.New("dial tcp: connection refused")

	assert.Same(t, in, WrapPgErr(in))
}

func TestWrapPgErr_Nil(t *testing.T) {
	assert.NoError(t, WrapPgErr(nil))
}

// errors.As 会顺着 %w 往里找，所以 PgError 被包过几层也要能认出来。
// pgx 的错误到达 WrapPgErr 时通常已经被包过。
func TestWrapPgErr_FindsPgErrorThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("insert knowledge base: %w",
		fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23505", ConstraintName: "c"}))

	assert.ErrorIs(t, WrapPgErr(wrapped), ErrDuplicateKey)
}

// 【防退化】约束名一旦和迁移里实际生成的对不上，分流会静默失效：
// 幂等键冲突会被当成普通的 409。
//
// PRIMARY KEY (endpoint, idempotency_key) 在 PostgreSQL 里默认生成
// <表名>_pkey。做幂等表那张迁移时，如果表名不叫 idempotency_keys，
// 或者改成了命名约束，这里必须同步改——这条测试会提醒你。
func TestConstraintNameMatchesMigrationConvention(t *testing.T) {
	require.Equal(t, "idempotency_keys_pkey", constraintIdempotencyKey,
		"要和 migrations 里 idempotency_keys 表的主键约束名一致")
}
