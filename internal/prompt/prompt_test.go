package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidate 断言注册表本身没有结构性错误。
//
// 它同时是"加新版本"的守门员：变量声明漏了、Active 指错了、key 重名，
// 都会在这里红，而不是等到某个端点渲染出空串。
func TestValidate(t *testing.T) {
	require.NoError(t, Validate())
}

func TestCatalog_SortedAndDeepCopied(t *testing.T) {
	catalog := Catalog()
	require.NotEmpty(t, catalog)
	for i := 1; i < len(catalog); i++ {
		require.Less(t, catalog[i-1].Key, catalog[i].Key, "Catalog 必须按 key 排序")
	}

	// 改动返回的副本不能影响下一次调用（注册表是审计对象，运行期不可变）。
	before := Catalog()[0].Title
	catalog[0].Title = "被改掉的标题"
	catalog[0].Versions[0].Content = "被改掉的内容"
	require.Equal(t, before, Catalog()[0].Title)
}

func TestContentHash_Deterministic(t *testing.T) {
	a := ContentHash("你是科研助手。")
	b := ContentHash("你是科研助手。")
	require.Equal(t, a, b)
	require.Len(t, a, 64, "SHA-256 十六进制应为 64 位")
	require.NotEqual(t, a, ContentHash("你是科研助手！"), "差一个标点也必须换哈希")
}

func TestRender_SubstitutesVariables(t *testing.T) {
	out, err := Render("translate.system", map[string]string{"targetLang": "English"})
	require.NoError(t, err)
	require.Contains(t, out, "English")
	require.NotContains(t, out, "{{", "渲染后不应残留占位符")

	out, err = Render("survey.user", map[string]string{"focus": "", "docs": "片段", "relations": "A 引用了 B"})
	require.NoError(t, err)
	require.Contains(t, out, "片段")
	require.Contains(t, out, "A 引用了 B")
}

func TestRender_MissingRequiredVariable(t *testing.T) {
	_, err := Render("translate.system", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "targetLang")

	_, err = Render("no.such.key", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown prompt key")
}

func TestRender_OptionalVariableMayBeEmpty(t *testing.T) {
	out, err := Render("survey.user", map[string]string{"docs": "d", "relations": "r"})
	require.NoError(t, err, "focus 是可选变量，不传也应能渲染")
	require.Contains(t, out, "关注方向：")
}

func TestWritingTaskText_AllKinds(t *testing.T) {
	for _, kind := range []string{"related_work", "summary", "outline", "framework"} {
		text, err := WritingTaskText(kind)
		require.NoError(t, err, kind)
		require.NotEmpty(t, text)
	}
	_, err := WritingTaskText("poster")
	require.Error(t, err)
}

func TestActiveVersionHasHashableContent(t *testing.T) {
	for _, e := range Catalog() {
		v := e.ActiveVersion()
		require.NotZero(t, v.Version, "%s: Active 版本不存在", e.Key)
		require.NotEmpty(t, ContentHash(v.Content))
	}
}

func TestVersionHistory_IsNonDecreasing(t *testing.T) {
	for _, e := range Catalog() {
		last := 0
		for _, v := range e.Versions {
			require.Greater(t, v.Version, last, "%s: 版本号必须严格递增", e.Key)
			last = v.Version
		}
		require.Equal(t, last, e.Active, "%s: Active 必须是最新版本", e.Key)
		require.NotEmpty(t, e.Versions[0].Content)
	}
}

func TestChatSystemPrompt_KeepsGroundingContract(t *testing.T) {
	// 第 2 版的核心契约：逐句 [n]、无证据拒答。这两句是 grounding 检查的前提，
	// 改提示词时若删掉它们，grounding 会变成永远无效的检查——这里钉死。
	content := ""
	for _, e := range Catalog() {
		if e.Key == "chat.system" {
			content = e.ActiveVersion().Content
		}
	}
	require.Contains(t, content, "[1]")
	require.Contains(t, content, "根据现有资料无法回答")
	require.True(t, strings.Contains(content, "不要编造引用编号"))
}
