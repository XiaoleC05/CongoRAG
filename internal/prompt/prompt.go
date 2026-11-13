// Package prompt 是提示词的注册表与渲染器（v6.3.0）。
//
// 【它解决什么问题】在 v6.2.0 之前，提示词是散落在 6 个文件里的字符串字面量：
// 对话系统提示在 conversation，压缩提示在 ctxmgr，翻译提示在 llm，写作/对比/
// 综述/图注在 HTTP handler。它们没有版本、没有哈希、没有变更记录，改一句话
// 不会留下任何痕迹——而"提示词就是产品行为"，评审要的正是它改过什么、为什么改。
//
// 这个包把提示词变成可审计的资产：
//   - 每个条目有稳定的 key、标题、说明、变量声明与代码位置；
//   - 每个版本有完整文本、SHA-256、变更原因、生效时间、采样参数与（可选）指标；
//   - 生产代码只能通过 Render 取文本，不再内联字面量。
//
// 【为什么不做成运行时可变配置】提示词是受版本管理的代码，不是用户配置。
// 允许运行时改会让"这份回答是哪版提示词生成的"无法追溯；要试验新版本就加一个
// Version，用评测集对比后再把 Active 指过去——改动进 git diff，可 review。
//
// 【依赖方向】本包是叶子：不 import 任何 internal 包，只做字符串。
package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind 是提示词在链路里的角色。
type Kind string

const (
	// KindSystem 是系统提示（权限最高的一层，决定模型的身份与硬约束）。
	KindSystem Kind = "system"
	// KindUser 是用户提示模板（通常负责把检索到的证据按编号排好）。
	KindUser Kind = "user"
)

// Variable 是模板里的一个占位符。
type Variable struct {
	Name        string
	Description string
	Required    bool
}

// CodeRef 指向"谁在用这个提示词"。
//
// file 用仓库内相对路径（评审/答辩时能直接点开），symbol 用函数或变量名。
type CodeRef struct {
	File   string
	Symbol string
}

// Sampling 是这个提示词在调用模型时使用的采样参数。
//
// 它只描述"代码实际传下去的"值；温度在 v6.3.0 之前被适配器丢弃过
// （见 internal/llm/eino.go 的 toEinoOptions 注释），所以这里是审计证据的一部分。
type Sampling struct {
	Temperature *float32
	MaxTokens   int
	Note        string
}

// Metrics 是这一版提示词的可复现效果记录（来自评测或固定用例）。
type Metrics struct {
	Name       string
	Value      string
	SampleSize int
	Note       string
}

// Version 是一个历史版本。
type Version struct {
	Version      int
	Content      string
	ChangeReason string
	CreatedAt    string // YYYY-MM-DD：提示词是人工评审的产物，日期粒度够用
	ActivatedAt  string
	Sampling     *Sampling
	Metrics      *Metrics
}

// Entry 是一条提示词资产。
type Entry struct {
	Key         string
	Title       string
	Description string
	Kind        Kind
	Variables   []Variable
	CodeRefs    []CodeRef
	Versions    []Version
	Active      int
}

// ContentHash 返回一段提示词文本的 SHA-256（小写十六进制）。
//
// 【为什么用全量 64 位而不是短哈希】它要能回答"线上跑的这份内容与仓库里
// 这一份是不是逐字节相同"，短哈希只适合展示。前端会把前 12 位显示出来，
// 完整值放在 title/复制按钮里。
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ActiveVersion 返回当前生效的版本（注册表校验保证存在）。
func (e Entry) ActiveVersion() Version {
	for _, v := range e.Versions {
		if v.Version == e.Active {
			return v
		}
	}
	return Version{}
}

// placeholderPattern 匹配 {{变量名}}。
var placeholderPattern = regexp.MustCompile("\\{\\{([a-zA-Z0-9_]+)\\}\\}")

// Render 渲染指定 key 的当前生效版本。
//
// 【错误而不是 panic】调用发生在 handler 里，变量缺失是"调用方写错了"，
// 应该变成一个可读的 500 而不是把进程带走。包级 var 初始化处用 MustRender。
func Render(key string, vars map[string]string) (string, error) {
	e, ok := lookup(key)
	if !ok {
		return "", fmt.Errorf("unknown prompt key %q", key)
	}
	content := e.ActiveVersion().Content
	declared := make(map[string]Variable, len(e.Variables))
	for _, v := range e.Variables {
		declared[v.Name] = v
	}
	var missing []string
	out := placeholderPattern.ReplaceAllStringFunc(content, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		v, known := declared[name]
		if !known {
			// 模板里出现了未声明的变量：这是注册表自身的错，返回空串让上层报错。
			missing = append(missing, name)
			return ""
		}
		val, provided := vars[name]
		if !provided && v.Required {
			missing = append(missing, name)
			return ""
		}
		if !provided {
			return ""
		}
		return val
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("render prompt %q: missing/unavailable variables %s", key, strings.Join(missing, ", "))
	}
	return out, nil
}

// MustRender 是 Render 的 panic 版本，只用于包级常量初始化。
func MustRender(key string, vars map[string]string) string {
	out, err := Render(key, vars)
	if err != nil {
		panic(err)
	}
	return out
}

// Catalog 返回注册表（按 key 排序）。
//
// 【返回深拷贝】调用方（HTTP handler）会把结果序列化出去；直接给内部切片，
// 将来任何一次"顺手 append"都会改到全局注册表。提示词是审计对象，
// 运行期不允许被改动。
func Catalog() []Entry {
	out := make([]Entry, 0, len(registry))
	for _, e := range registry {
		c := e
		c.Variables = append([]Variable(nil), e.Variables...)
		c.CodeRefs = append([]CodeRef(nil), e.CodeRefs...)
		c.Versions = make([]Version, len(e.Versions))
		for i, v := range e.Versions {
			vv := v
			if v.Sampling != nil {
				s := *v.Sampling
				vv.Sampling = &s
			}
			if v.Metrics != nil {
				m := *v.Metrics
				vv.Metrics = &m
			}
			c.Versions[i] = vv
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// WritingTaskText 返回某种体例的任务指令文本（writing.task.* 之一）。
func WritingTaskText(kind string) (string, error) {
	key, ok := writingTaskKeys[kind]
	if !ok {
		return "", fmt.Errorf("unknown writing kind %q", kind)
	}
	return Render(key, nil)
}

func lookup(key string) (Entry, bool) {
	for _, e := range registry {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

func f32(v float32) *float32 { return &v }

// writingTaskKeys 是 writing 的四种体例到注册表 key 的映射。
var writingTaskKeys = map[string]string{
	"related_work": "writing.task.related_work",
	"summary":      "writing.task.summary",
	"outline":      "writing.task.outline",
	"framework":    "writing.task.framework",
}

// registry 是全部提示词的唯一真相。
//
// 【加一个版本的正确流程】先把新版本追加到 Versions（写清 changeReason 与
// 评测/用例效果），把 Active 指过去，再在同一个提交里更新 docs/prompt-audit.md；
// 两步缺一不可——只改代码不改文档，下一轮审计就不知道这次为什么改。
var registry = []Entry{
	{
		Key:         "chat.system",
		Title:       "科研问答系统提示",
		Description: "对话链路的系统提示：只依据检索片段回答，每个事实句必须带 [n]，无证据时固定拒答。",
		Kind:        KindSystem,
		CodeRefs: []CodeRef{
			{File: "internal/conversation/usecase.go", Symbol: "defaultSystemPrompt"},
			{File: "internal/grounding/grounding.go", Symbol: "Verify"},
		},
		Versions: []Version{
			{
				Version:      1,
				Content:      "你是 CongoRAG 的助手。基于提供的检索片段回答用户问题，如果片段里没有相关信息，如实说明，不要编造。",
				ChangeReason: "通用 RAG 的默认提示：先说身份，再要求不编造。",
				CreatedAt:    "2026-10-01",
				ActivatedAt:  "2026-10-01",
			},
			{
				Version: 2,
				Content: "你是 CongoRAG 的科研助手。只依据提供的检索片段回答用户问题。" +
					"回答里的每一句话末尾都必须用方括号标注它依赖的片段编号，例如 [1]、[2]（编号就是片段前面的序号，从 1 开始）；" +
					"不要输出任何没有引用编号的句子。片段里没有相关信息时，直接回答「根据现有资料无法回答」，" +
					"不要使用片段之外的知识，不要编造引用编号。",
				ChangeReason: "让引用落地检查（grounding）有可检查的对象：每个事实句必须带合法 [n]；" +
					"同时把不知道变成明确的拒答行为，而不是赌模型会不会编。",
				CreatedAt:   "2026-10-29",
				ActivatedAt: "2026-10-29",
				Metrics: &Metrics{
					Name:       "grounding.valid",
					Value:      "true",
					SampleSize: 2,
					Note:       "DeepSeek-V3 实测 2/2 句带合法引用；Qwen2.5-7B 仍会漏标（模型能力差异，见技术文档选型）",
				},
			},
		},
		Active: 2,
	},
	{
		Key:         "chat.compress",
		Title:       "上下文压缩系统提示",
		Description: "超预算时把较旧的历史压成一段摘要；不分级，一次压完。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "internal/ctxmgr/compressor.go", Symbol: "compressPrompt"}},
		Versions: []Version{{
			Version:      1,
			Content:      "请把下面这段对话历史压缩成一段简洁的摘要，保留关键事实、决定和用户表达过的偏好，去掉寒暄和重复内容。直接输出摘要正文，不要加任何前缀说明。",
			ChangeReason: "从 ctxmgr/compressor.go 的内联常量收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
			Sampling:     &Sampling{Note: "未显式覆盖温度（适配器默认）"},
		}},
		Active: 1,
	},
	{
		Key:         "chat.compress.budget_suffix",
		Title:       "上下文压缩：尺寸约束后缀",
		Description: "拼在压缩提示后面的 token 预算约束；不单独使用。",
		Kind:        KindSystem,
		Variables: []Variable{
			{Name: "targetTokens", Description: "可压缩区还允许占用的 token 数", Required: true},
		},
		CodeRefs: []CodeRef{{File: "internal/ctxmgr/compressor.go", Symbol: "compressBudgetPrompt"}},
		Versions: []Version{{
			Version:      1,
			Content:      "这段摘要控制在 {{targetTokens}} 个 token 以内。",
			ChangeReason: "把尺寸约束从代码拼接改成注册表模板；行为不变，但约束文本现在可被审计。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "translate.system",
		Title:       "论文翻译系统提示",
		Description: "阅读器整页/划词翻译：保留公式、术语、引用编号与数字原文，只输出译文。",
		Kind:        KindSystem,
		Variables: []Variable{
			{Name: "targetLang", Description: "目标语言，如 zh-CN / English", Required: true},
		},
		CodeRefs: []CodeRef{{File: "internal/llm/usecase.go", Symbol: "Translate"}},
		Versions: []Version{{
			Version: 1,
			Content: "你是学术论文翻译助手。把用户给出的论文片段准确翻译成 {{targetLang}}；保留公式、术语、引用编号与数字原文；只输出译文，不要解释或补充。",
			ChangeReason: "从 llm/usecase.go 的字符串拼接收敛到注册表；temperature=0.1 的采样参数随版本一起登记" +
				"（v6.3.0 起适配器真正把它传给模型，之前被丢弃）。",
			CreatedAt:   "2026-11-14",
			ActivatedAt: "2026-11-14",
			Sampling:    &Sampling{Temperature: f32(0.1), Note: "学术翻译不允许改写数字/编号，低温度减少自由发挥"},
			Metrics: &Metrics{
				Name:       "translate.keep_numbers",
				Value:      "true",
				SampleSize: 1,
				Note:       "Attention 摘要整页翻译：编号/公式保留（人工核对）",
			},
		}},
		Active: 1,
	},
	{
		Key:         "writing.system",
		Title:       "写作 Copilot 系统提示",
		Description: "只依据编号资料写作，每个事实句带 [n]，资料不足直接说明。",
		Kind:        KindSystem,
		CodeRefs: []CodeRef{
			{File: "internal/prompt/prompt.go", Symbol: "registry"},
			{File: "apps/api/internal/api/handler_research.go", Symbol: "buildWritingPrompt"},
		},
		Versions: []Version{{
			Version: 1,
			Content: "你是科研写作助手。只依据给出的编号资料写作；每个事实句末尾用 [n] 标注来源编号；" +
				"输出 Markdown，不要输出与写作任务无关的解释。资料不足时直接说明「根据现有资料无法完成」，不要编造。",
			ChangeReason: "从 handler 的 buildWritingPrompt 收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
			Sampling:     &Sampling{Temperature: f32(0.3), Note: "写作需要一点创造力但不能跑题"},
			Metrics: &Metrics{
				Name:       "writing.citations_aligned",
				Value:      "true",
				SampleSize: 1,
				Note:       "759 字相关工作草稿：[n] 与引用列表一一对应（人工核对）",
			},
		}},
		Active: 1,
	},
	{
		Key:         "writing.user",
		Title:       "写作 Copilot 用户提示模板",
		Description: "把主题、体例任务与编号证据拼成一次写作请求。",
		Kind:        KindUser,
		Variables: []Variable{
			{Name: "topic", Description: "写作主题", Required: true},
			{Name: "task", Description: "体例任务（由 writing.task.* 渲染）", Required: true},
			{Name: "evidence", Description: "编号证据块（[n] + 正文 + 文件名/页码）", Required: true},
		},
		CodeRefs: []CodeRef{{File: "apps/api/internal/api/handler_research.go", Symbol: "buildWritingPrompt"}},
		Versions: []Version{{
			Version:      1,
			Content:      "写作主题：{{topic}}\r\n\r\n任务：{{task}}。\r\n\r\n可用资料（每条前面的编号就是引用编号）：\r\n{{evidence}}",
			ChangeReason: "把写作请求的用户侧模板从代码拼接改为注册表模板；证据块的内容仍由 handler 按 citation 顺序生成。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "writing.task.related_work",
		Title:       "写作体例：相关工作",
		Description: "300~500 字相关工作综述，按方法脉络组织。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "internal/prompt/prompt.go", Symbol: "writingTaskKeys"}},
		Versions: []Version{{
			Version:      1,
			Content:      "写一段 300~500 字的「相关工作」综述，按方法脉络组织（不要逐篇罗列），指出各方法的差异与联系",
			ChangeReason: "从 handler 的 switch 分支收敛到注册表，让体例任务也进入审计范围。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "writing.task.summary",
		Title:       "写作体例：摘要",
		Description: "200~300 字摘要式总结。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "internal/prompt/prompt.go", Symbol: "writingTaskKeys"}},
		Versions: []Version{{
			Version:      1,
			Content:      "写一段 200~300 字的摘要式总结，概括这些资料的核心方法与结论",
			ChangeReason: "从 handler 的 switch 分支收敛到注册表。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "writing.task.outline",
		Title:       "写作体例：提纲",
		Description: "章节提纲（Markdown 多级列表）。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "internal/prompt/prompt.go", Symbol: "writingTaskKeys"}},
		Versions: []Version{{
			Version:      1,
			Content:      "写一份章节提纲（Markdown 多级列表），覆盖资料涉及的主要主题与它们之间的关系",
			ChangeReason: "从 handler 的 switch 分支收敛到注册表。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "writing.task.framework",
		Title:       "写作体例：完整论文框架",
		Description: "Abstract 到 References 的完整框架，References 只列真实文献。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "internal/prompt/prompt.go", Symbol: "writingTaskKeys"}},
		Versions: []Version{{
			Version: 1,
			Content: "写一份完整的论文框架（Markdown）：包含 Abstract、Introduction、Related Work、" +
				"Method（分点提纲）、Experiments（分点提纲）、Conclusion 与 References；" +
				"References 只能列资料中出现过的真实文献，写不出完整条目时保留可核对的标题/年份/来源，不要编造",
			ChangeReason: "从 handler 的 switch 分支收敛到注册表。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "compare.system",
		Title:       "多篇对比系统提示",
		Description: "只依据编号资料，用 Markdown 表格对比观点/方法/证据，表格后给综合结论。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "apps/api/internal/api/handler_research.go", Symbol: "buildComparePrompt"}},
		Versions: []Version{{
			Version: 1,
			Content: "你是科研文献对比助手。只依据给出的编号资料回答；" +
				"用 Markdown 表格对比不同论文/不同来源在同一问题上的观点、方法与证据（列可以按主题自拟），" +
				"表格后用 2~3 句给综合结论；每个事实句末尾用 [n] 标注来源编号；" +
				"资料不足时直接说明「根据现有资料无法完成对比」，不要编造。",
			ChangeReason: "从 handler 的 buildComparePrompt 收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
			Sampling:     &Sampling{Temperature: f32(0.3), Note: "与写作共用 Generate 的默认温度"},
		}},
		Active: 1,
	},
	{
		Key:         "compare.user",
		Title:       "多篇对比用户提示模板",
		Description: "对比问题 + 编号资料（含来源文件与页码）。",
		Kind:        KindUser,
		Variables: []Variable{
			{Name: "question", Description: "对比问题", Required: true},
			{Name: "evidence", Description: "编号证据块（[n] + 来源 + 正文）", Required: true},
		},
		CodeRefs: []CodeRef{{File: "apps/api/internal/api/handler_research.go", Symbol: "buildComparePrompt"}},
		Versions: []Version{{
			Version:      1,
			Content:      "对比问题：{{question}}\r\n\r\n资料（每条前面的编号就是引用编号，括号里是来源文件与页码）：\r\n{{evidence}}",
			ChangeReason: "从代码拼接改为注册表模板；编号顺序仍由 handler 保证与 citations 一致。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "survey.system",
		Title:       "引用综述系统提示",
		Description: "基于引用图与片段写领域综述 + 仍开放的 Future Work，不编造。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "apps/api/internal/api/handler_research.go", Symbol: "GetCitationSurvey"}},
		Versions: []Version{{
			Version:      1,
			Content:      "你是学术综述助手。基于给定的库内论文片段与引用关系，用中文写一份 400-600 字的领域综述（Markdown，包含研究脉络、代表工作、引用关系），然后给出 3 条 Future Work 方向。对于 Future Work，只列出结合论文标题/编号年份线索判断仍开放的方向；疑似已被后续论文覆盖的不要列出，无法判断的最多保留一条并标注「不确定」；只能使用给定材料，不要编造未出现的论文或结论；材料不足时明确说明。",
			ChangeReason: "从 handler 内联字符串收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
			Sampling:     &Sampling{Temperature: f32(0.3)},
		}},
		Active: 1,
	},
	{
		Key:         "survey.user",
		Title:       "引用综述用户提示模板",
		Description: "关注方向 + 论文片段 + 引用关系。",
		Kind:        KindUser,
		Variables: []Variable{
			{Name: "focus", Description: "用户关注方向，可为空", Required: false},
			{Name: "docs", Description: "论文片段块（标题/年份 + 正文摘录）", Required: true},
			{Name: "relations", Description: "引用关系列表", Required: true},
		},
		CodeRefs: []CodeRef{{File: "apps/api/internal/api/handler_research.go", Symbol: "GetCitationSurvey"}},
		Versions: []Version{{
			Version:      1,
			Content:      "关注方向：{{focus}}\r\n\r\n论文片段：\r\n{{docs}}\r\n引用关系：\r\n{{relations}}\r\n请输出综述 + Future Work。",
			ChangeReason: "从代码拼接改为注册表模板；片段摘录长度（700 rune）仍由 handler 决定。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
	{
		Key:         "caption.system",
		Title:       "Figure Caption 系统提示",
		Description: "根据图表数据写一句中文学术图注，不改数据。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "apps/api/internal/api/handler_research.go", Symbol: "figureCaption"}},
		Versions: []Version{{
			Version:      1,
			Content:      "你是学术写作助手。根据给定的图表数据，写一句中文 Figure Caption：说明图表类型、横纵轴变量、主要趋势与极值；不要 Markdown、不要解释。",
			ChangeReason: "从 handler 内联字符串收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
			Sampling:     &Sampling{Temperature: f32(0.3)},
		}},
		Active: 1,
	},
	{
		Key:         "versiondiff.system",
		Title:       "论文版本对比系统提示",
		Description: "只根据两版开头片段列出 3-5 条修订要点，片段不足时明确说明。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "apps/api/internal/api/handler_knowledge.go", Symbol: "DiffDocumentVersions"}},
		Versions: []Version{{
			Version:      1,
			Content:      "你是学术论文版本对比助手。只根据给出的两版开头片段，用中文列出 3-5 条修订要点（Markdown 无序列表）；片段不足以判断时明确说明，不要编造。",
			ChangeReason: "从 handler 内联字符串收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
			Sampling:     &Sampling{Temperature: f32(0.3)},
		}},
		Active: 1,
	},
	{
		Key:         "chat.compress.urgent_suffix",
		Title:       "上下文压缩：极限约束后缀",
		Description: "可压缩区预算已被占满时使用的后缀，要求摘要尽可能短。",
		Kind:        KindSystem,
		CodeRefs:    []CodeRef{{File: "internal/ctxmgr/compressor.go", Symbol: "compressBudgetPrompt"}},
		Versions: []Version{{
			Version:      1,
			Content:      "这段摘要必须尽可能短。",
			ChangeReason: "从 compressBudgetPrompt 的分支字面量收敛到注册表（v6.3.0 首版）；文本一字未改。",
			CreatedAt:    "2026-11-14",
			ActivatedAt:  "2026-11-14",
		}},
		Active: 1,
	},
}

// Validate 检查注册表自身的完整性，返回所有问题而不是遇到第一个就返回。
//
// 【为什么在运行期也需要它】提示词是"数据 + 约定"：新增版本时很容易忘记同步
// 变量声明、把 Active 指向不存在的版本、或者让两条 key 重名。这些错误都不会
// 编译失败，只会在某个端点上静默地少渲染一段或渲染成空串。API 启动装配时
// 调用一次（见 apps/api/internal/app），失败就拒绝启动——和数据库连不上、
// tiktoken 词表下载失败同一条原则：配错了就在启动时说。
func Validate() error {
	var problems []string
	seen := map[string]bool{}
	for _, e := range registry {
		if e.Key == "" || e.Title == "" || e.Description == "" {
			problems = append(problems, fmt.Sprintf("entry %q: key/title/description 不能为空", e.Key))
		}
		if seen[e.Key] {
			problems = append(problems, fmt.Sprintf("重复的 key: %q", e.Key))
		}
		seen[e.Key] = true

		if len(e.Versions) == 0 {
			problems = append(problems, fmt.Sprintf("%s: 没有任何版本", e.Key))
			continue
		}
		if e.Active != len(e.Versions) {
			problems = append(problems, fmt.Sprintf("%s: Active=%d，但最新版本是 %d；生效版本必须是最后一个已评审版本",
				e.Key, e.Active, len(e.Versions)))
		}
		declared := map[string]bool{}
		for _, v := range e.Variables {
			if v.Name == "" || v.Description == "" {
				problems = append(problems, fmt.Sprintf("%s: 变量缺少 name/description", e.Key))
			}
			if declared[v.Name] {
				problems = append(problems, fmt.Sprintf("%s: 变量 %q 重复声明", e.Key, v.Name))
			}
			declared[v.Name] = true
		}
		for i, v := range e.Versions {
			if v.Version != i+1 {
				problems = append(problems, fmt.Sprintf("%s: 版本号不连续（第 %d 个是 v%d）", e.Key, i+1, v.Version))
			}
			if strings.TrimSpace(v.Content) == "" {
				problems = append(problems, fmt.Sprintf("%s v%d: 内容为空", e.Key, v.Version))
			}
			if strings.TrimSpace(v.ChangeReason) == "" || strings.TrimSpace(v.CreatedAt) == "" || strings.TrimSpace(v.ActivatedAt) == "" {
				problems = append(problems, fmt.Sprintf("%s v%d: 变更原因/创建时间/生效时间不能为空", e.Key, v.Version))
			}
			// 生效版本的占位符必须与变量声明一一对应：少声明会在渲染时留下
			// 未替换的 {{x}}，多声明会让审计页显示一个永远用不到的变量。
			if v.Version == e.Active {
				inTemplate := map[string]bool{}
				for _, m := range placeholderPattern.FindAllStringSubmatch(v.Content, -1) {
					inTemplate[m[1]] = true
				}
				for name := range inTemplate {
					if !declared[name] {
						problems = append(problems, fmt.Sprintf("%s v%d: 模板占位符 {{%s}} 未在 Variables 里声明", e.Key, v.Version, name))
					}
				}
				for name := range declared {
					if !inTemplate[name] {
						problems = append(problems, fmt.Sprintf("%s v%d: 变量 %q 已声明但模板里没有占位符", e.Key, v.Version, name))
					}
				}
			}
		}
		if len(e.CodeRefs) == 0 {
			problems = append(problems, fmt.Sprintf("%s: 至少需要一个代码位置（评审要能点到实现）", e.Key))
		}
		for _, ref := range e.CodeRefs {
			if strings.TrimSpace(ref.File) == "" || strings.TrimSpace(ref.Symbol) == "" {
				problems = append(problems, fmt.Sprintf("%s: 代码位置缺少 file/symbol", e.Key))
			}
		}
	}
	for kind, key := range writingTaskKeys {
		if _, ok := lookup(key); !ok {
			problems = append(problems, fmt.Sprintf("writingTaskKeys[%q] 指向不存在的 key %q", kind, key))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("prompt registry 校验失败：%s", strings.Join(problems, "；"))
	}
	return nil
}
