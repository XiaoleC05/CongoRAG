// 真库验证 issue #120 的 CHECK 约束：文本枚举列写进非法值必须被数据库
// 拒绝，而不是静默落库。
//
// 【为什么必须连真库】约束是 migrations/0013_enum_constraints.up.sql 里的
// 一句 DDL，应用层看不见它到底建上没有。不连真库的话，把那条
// ADD CONSTRAINT 整句删掉、或者取值清单写漏一个，全套单测仍然全绿。
//
// 【脏值的后果不是报错，是静默的错行为】以 documents.status 为例：落进
// 'Ready' 这类值之后，ProcessDocument 的 switch 掉进 default 分支永远返回
// ErrConflict，文档既不在 reindexableStatuses 里、也无法重新排队，UI 上
// 永远停在"处理中"（internal/knowledge/usecase.go）。所以这条约束值得有
// 一条测试钉住——它挡的正是"将来某次运维脚本或新代码把状态值写歪"。
//
// 【门控】和同包的 postgres_integration_test.go 共用 requireTestDB
// （internal/testdb：设了 CONGORAG_TEST_DB_URL 用它，没设就自己起容器）。
package knowledge

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enumProbeKnowledgeBase 造一个带清理钩子的知识库，供探针文档当外键用
// （documents.knowledge_base_id 是指向它的外键，文档会跟着级联删掉）。
func enumProbeKnowledgeBase(t *testing.T, pool *pgxpool.Pool, ctx context.Context) uuid.UUID {
	t.Helper()
	kbID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO knowledge_bases (id, name) VALUES ($1, $2)`, kbID, "enum-probe")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, kbID)
	})
	return kbID
}

// 【为什么直接发原始 INSERT，不走 PgDocRepo】要验证的正是数据库那一层的
// 约束，插进去的又是应用层永远不会写的非法值——绕开 repo 才看得见"约束
// 本身在不在"。走 repo 的话，任何一层应用校验都可能替数据库挡下这一插，
// 测试就变成在测应用校验了。
//
// 【为什么两个方向都测】只测"非法被拒"，把约束写成 `CHECK (false)` 也能
// 绿——而那样每一次正常上传都会失败。合法值必须插得进去 + 非法值必须被
// 拒，两条合起来才拦得住"约束过宽"和"过窄"两种写错。
func TestIntegration_DocumentsStatus_ConstraintRejectsValuesOutsideTheEnum(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	kbID := enumProbeKnowledgeBase(t, pool, ctx)

	for _, status := range []string{"queued", "processing", "ready", "failed"} {
		_, err := pool.Exec(ctx,
			`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status)
			 VALUES ($1, $2, 'doc.md', $3, $4)`,
			uuid.New(), kbID, "enum-probe-"+uuid.NewString(), status)
		assert.NoError(t, err, "status=%q 是常量表里的合法值，必须能插进去", status)
	}

	// 'Ready'（首字母大写）是真实会从"手写 SQL 的运维脚本"里冒出来的那一类
	// 脏值——应用层的常量表全是小写。它恰好能证明挡下这一插的是数据库，
	// 而不是任何一层应用校验。
	_, err := pool.Exec(ctx,
		`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status)
		 VALUES ($1, $2, 'doc.md', $3, 'Ready')`,
		uuid.New(), kbID, "enum-probe-"+uuid.NewString())

	require.Error(t, err, "documents.status 的 CHECK 约束必须拒绝枚举之外的值")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23514", pgErr.Code, "应当是 check_violation，而不是别的错误")
	assert.Equal(t, "documents_status_check", pgErr.ConstraintName,
		"约束名要和 0013 迁移里写的一致——按约束名分流的代码靠它定位")
}
