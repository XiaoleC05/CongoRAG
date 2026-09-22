package platform

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// tool_effect_log 的唯一冲突是第三种 23505 语义（issue #63）：
// 它不是"这次请求不该成功"，而是"这个副作用已经发生过了"。
// 恢复路径靠它决定跳过重放——被错分成 ErrDuplicateKey 的话，
// resume 的判据就没了，工具会被重复执行而且不报错。
func TestWrapPgErr_ToolEffectAppliedIsNotDuplicateKey(t *testing.T) {
	in := &pgconn.PgError{Code: "23505", ConstraintName: constraintToolEffectLog}

	out := WrapPgErr(in)

	assert.ErrorIs(t, out, ErrToolEffectApplied)
	assert.NotErrorIs(t, out, ErrDuplicateKey, "工具效果已生效不该被当成业务冲突")
	assert.NotErrorIs(t, out, ErrIdempotentHit, "它和幂等命中是两件事")
}

// 【防退化】约束名一旦和迁移文件里写的对不上，分流会静默失效：
// 幂等键冲突会被当成普通的 409，工具效果冲突会变成一次假的 500。
//
// 【为什么读文件而不是再断言一次字面量】断言常量等于它自己永远通过，
// 拦不住"迁移里改了约束名、常量没跟着改"这个真实故障。这里直接去
// migrations/ 里找那个名字——它才是真相。
func TestConstraintNamesMatchMigrations(t *testing.T) {
	// 本测试文件在 internal/platform/，迁移在仓库根的 migrations/。
	migrations := filepath.Join("..", "..", "migrations")

	cases := []struct {
		name  string
		files []string
	}{
		// idempotency_keys_pkey 是 Postgres 给 PRIMARY KEY (endpoint,
		// idempotency_key) 生成的默认名，迁移里不会出现这个字面量，
		// 所以这里查的是"表名 + 主键声明"这个组合。
		{name: "idempotency_keys_pkey", files: []string{
			filepath.Join(migrations, "0003_conversations.up.sql"),
		}},
		// tool_effect_log 的约束是显式命名的，名字必须能在迁移里找到。
		{name: constraintToolEffectLog, files: []string{
			filepath.Join(migrations, "0010_tool_effect_log.up.sql"),
		}},
	}

	// constraintIdempotencyKey 必须仍然是那个默认名形状——改表的写法就会失配。
	require.Equal(t, "idempotency_keys_pkey", constraintIdempotencyKey,
		"要和 migrations 里 idempotency_keys 表的主键约束名一致")

	for _, tc := range cases {
		found := false
		for _, f := range tc.files {
			body, err := os.ReadFile(f)
			require.NoError(t, err, "读不到迁移文件 %s", f)
			// 幂等表那条查的是表名（默认约束名由它派生），
			// 工具效果那条查的就是约束名字面量。
			needle := tc.name
			if needle == constraintIdempotencyKey {
				needle = "idempotency_keys"
			}
			if bytes.Contains(body, []byte(needle)) {
				found = true
			}
		}
		assert.True(t, found, "迁移文件里找不到 %s——constraint 名字漂了，23505 分流会静默失效", tc.name)
	}
}
