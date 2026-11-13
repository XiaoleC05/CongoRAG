package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/prompt"
)

// TestListPrompts_ReturnsFullCatalog 守住"审计页面的数据源完整"这条契约：
// 注册表里每一条都要出现在响应里，每条都带可校验的 SHA-256 与生效版本。
//
// 【为什么这个端点值得一条 handler 测试】它不碰数据库、不调模型，却决定了
// 评审/答辩时能否看到"提示词全文 + 哈希 + 变更原因"。字段少一个不会报错，
// 只会让页面某一栏静默为空。
func TestListPrompts_ReturnsFullCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	s := NewServer(Deps{})
	router.GET("/api/v1/prompts", s.ListPrompts)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/prompts", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var got []PromptEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	catalog := prompt.Catalog()
	require.Len(t, got, len(catalog), "响应必须覆盖注册表全部条目")

	byKey := make(map[string]PromptEntry, len(got))
	prev := ""
	for _, e := range got {
		require.NotEmpty(t, e.Key)
		require.NotEmpty(t, e.Versions)
		require.Equal(t, e.ActiveVersion, e.Versions[len(e.Versions)-1].Version,
			"%s: activeVersion 应指向最后一个版本", e.Key)
		require.Len(t, e.Versions[len(e.Versions)-1].ContentHash, 64,
			"%s: 内容哈希必须是完整 SHA-256", e.Key)
		require.NotEmpty(t, e.Versions[len(e.Versions)-1].Content)
		require.NotEmpty(t, e.CodeRefs, "%s: 必须有代码位置", e.Key)
		require.Greater(t, e.Key, prev, "响应应按 key 字典序")
		prev = e.Key
		byKey[e.Key] = e
	}

	// 逐字节核对哈希：界面上的"哈希"必须真的是界面上那份内容的哈希。
	for _, e := range catalog {
		active := e.ActiveVersion()
		require.Equal(t, prompt.ContentHash(active.Content), byKey[e.Key].Versions[len(e.Versions)-1].ContentHash)
	}
}
