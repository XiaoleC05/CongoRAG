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

// TestValidateRegistry_CatchesBrokenEntries 用表驱动证明每一条校验规则都会真的失败。
//
// 【为什么不能只测 Validate()==nil】那只证明"当前注册表是对的"，不证明
// "校验器能抓住错误"。变异体测试让每条规则都有一个会红的反例；将来有人
// 为了"让测试过"放宽规则时，对应的反例会立刻变绿暴露出来。
func TestValidateRegistry_CatchesBrokenEntries(t *testing.T) {
	// 取一条真实条目做底，逐字段造坏。
	// 【必须深拷贝】registry 的 Versions 是切片；按值返回 Entry 时切片仍与
	// 全局注册表共享底层数组——某个变异用例改了版本号，后面的用例会一起看到
	// 被改过的全局状态（第一版测试就是这么红的）。
	base := func() Entry {
		for _, e := range registry {
			if e.Key == "translate.system" {
				c := e
				c.Versions = append([]Version(nil), e.Versions...)
				c.Variables = append([]Variable(nil), e.Variables...)
				c.CodeRefs = append([]CodeRef(nil), e.CodeRefs...)
				return c
			}
		}
		t.Fatal("fixture entry translate.system not found")
		return Entry{}
	}

	cases := []struct {
		name   string
		mutate func([]Entry) []Entry
		want   string // 错误里应包含的片段
	}{
		{
			name: "重复 key",
			mutate: func(es []Entry) []Entry {
				dup := es[0]
				return append(es, dup)
			},
			want: "重复的 key",
		},
		{
			name: "Active 指向旧版本",
			mutate: func(es []Entry) []Entry {
				e := base()
				v2 := e.Versions[0]
				v2.Version = 2
				e.Versions = append(e.Versions, v2)
				e.Active = 1 // 最新是 2，却生效 1
				return []Entry{e}
			},
			want: "Active",
		},
		{
			name: "版本号不连续",
			mutate: func(es []Entry) []Entry {
				e := base()
				e.Versions[0].Version = 3
				e.Active = 3
				return []Entry{e}
			},
			want: "版本号不连续",
		},
		{
			name: "内容为空",
			mutate: func(es []Entry) []Entry {
				e := base()
				e.Versions[0].Content = "   "
				return []Entry{e}
			},
			want: "内容为空",
		},
		{
			name: "模板占位符未声明",
			mutate: func(es []Entry) []Entry {
				e := base()
				e.Versions[0].Content = "带有 {{undeclared}} 的模板"
				return []Entry{e}
			},
			want: "未在 Variables 里声明",
		},
		{
			name: "声明了变量但模板里没有占位符",
			mutate: func(es []Entry) []Entry {
				e := base()
				e.Variables = append(e.Variables, Variable{Name: "unused", Description: "没用到的变量"})
				return []Entry{e}
			},
			want: "已声明但模板里没有占位符",
		},
		{
			name: "没有代码位置",
			mutate: func(es []Entry) []Entry {
				e := base()
				e.CodeRefs = nil
				return []Entry{e}
			},
			want: "至少需要一个代码位置",
		},
		{
			name: "变更原因为空",
			mutate: func(es []Entry) []Entry {
				e := base()
				e.Versions[0].ChangeReason = ""
				return []Entry{e}
			},
			want: "变更原因",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			broken := c.mutate([]Entry{base()})
			err := validateRegistry(broken)
			require.Error(t, err, "变异体必须被校验器抓到")
			require.Contains(t, err.Error(), c.want)
		})
	}
}
