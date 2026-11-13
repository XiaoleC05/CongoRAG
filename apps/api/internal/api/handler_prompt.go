// Prompt 注册表端点（v6.3.0）：把提示词作为可审计资产暴露给界面。
//
// 【为什么这是一个只读端点】提示词是受版本管理的代码，不是用户配置：
// 允许通过 HTTP 改会让"这条回答是哪版提示词生成的"无法追溯。这里只读，
// 写操作是改 internal/prompt/prompt.go 并走 review / 评测。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/prompt"
)

// ListPrompts 返回全部提示词条目（按 key 字典序）。
//
// 响应里带每个版本的完整文本与 SHA-256：评审/答辩时可以当场对比
// "界面上这份内容"与"仓库里这份内容"是不是逐字节相同。
// 注册表在启动时已由 prompt.Validate() 校验过（见 apps/api/internal/app），
// 所以这里不再做结构性检查，只做类型转换。
func (s *Server) ListPrompts(c *gin.Context) {
	entries := prompt.Catalog()
	out := make([]PromptEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, toAPIPromptEntry(e))
	}
	c.JSON(http.StatusOK, out)
}
