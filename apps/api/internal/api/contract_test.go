package api

// 契约里声明的状态码，和 classify 实际会返回的状态码，是两处独立写下的
// 同一个约定——这里那条约定只有一条：父行不存在（ErrNotFound / ErrForeignKey）
// 一律映射成 404（见 problem.go 的 classify）。契约漏写一个状态码，不会有
// 任何东西报错，只是让按声明状态集写代码的调用方少处理一个分支。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractPath 从 go test 的工作目录（包目录）往上找仓库根的
// contracts/openapi.yaml。
//
// 【为什么不写死 "../../../.."】层数写死的话，这个包一挪位置测试就变成
// "file not found" 的假失败；往上找只依赖那个文件在仓库里的相对位置。
func contractPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		p := filepath.Join(dir, "contracts", "openapi.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("从测试目录往上找不到 contracts/openapi.yaml")
		}
		dir = parent
	}
}

// contractOperationResponses 取出某个 operationId 声明的响应码集合。
//
// 【为什么手切缩进而不引 YAML 解析器】go.mod 里现有的几个 yaml 包都是别人的
// 间接依赖，为了这条测试把它们提升成直接依赖，go.mod / go.sum 都要跟着动，
// 而 CI 的 go job 会跑 `go mod tidy` + `git diff --exit-code`。这份契约的
// 缩进是固定的（paths → 方法 → operationId 与 responses → 响应码，逐级两格），
// 按缩进切足够稳；切错了下面的断言会直接失败，不会静默通过。
func contractOperationResponses(t *testing.T, operationID string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(contractPath(t))
	require.NoError(t, err)

	// operationId 那一行缩进 6 格，响应码那一行缩进 8 格：
	//
	//	      operationId: uploadDocument
	//	      responses:
	//	        '202':
	const (
		opIndent   = 6
		codeIndent = 8
	)

	codes := map[string]bool{}
	inOperation := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if strings.HasPrefix(trimmed, "operationId:") {
			inOperation = strings.TrimSpace(strings.TrimPrefix(trimmed, "operationId:")) == operationID
			continue
		}
		if !inOperation {
			continue
		}
		// 缩进回到方法那一级（或更浅）＝这个 operation 结束了。
		// 空行和注释不算——契约里在 responses 中间插了一行注释。
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && indent < opIndent {
			break
		}
		if indent != codeIndent {
			continue
		}
		// 响应码在契约里是带引号的：'404':
		code, ok := strings.CutPrefix(trimmed, "'")
		if !ok {
			continue
		}
		if code, _, found := strings.Cut(code, "'"); found {
			codes[code] = true
		}
	}
	return codes
}

// 【这条测试拦的是什么】契约写"这个操作只会返回 202/400/500"，而它真的会
// 返回 404——按声明状态集写重试 / 跳转 / 日志分支的调用方会漏掉那个分支。
// 三个操作都会碰到"父行不存在"：上传到已删除的知识库（外键违规）、删除
// 不存在的文档（ErrNoRows）、给已删除的知识库建会话（外键违规），而
// classify 把这两种情况都映射成 404（issue #25）。
//
// 相邻的 getDocument 一直声明着 404，说明这不是有意的约定，是漏写。
func TestContractDeclares404WhenParentRowIsMissing(t *testing.T) {
	cases := []struct {
		operationID string
		why         string
	}{
		{"uploadDocument", "knowledge base 不存在时 documents 的外键插入失败"},
		{"deleteDocument", "文档不存在时 docRepo.Delete 返回 ErrNotFound"},
		{"createConversation", "knowledgeBaseId 指向的 knowledge base 不存在时外键插入失败"},
	}

	for _, tt := range cases {
		t.Run(tt.operationID, func(t *testing.T) {
			codes := contractOperationResponses(t, tt.operationID)
			require.NotEmpty(t, codes, "契约里找不到 operationId %s 的 responses", tt.operationID)

			assert.True(t, codes["404"],
				"%s 会返回 404（%s），契约必须声明它——classify 把 ErrNotFound 和外键违规都映射成 404",
				tt.operationID, tt.why)
		})
	}
}
