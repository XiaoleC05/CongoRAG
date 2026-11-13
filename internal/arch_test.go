// Package internal 只承载 internal/ 的架构守卫测试（arch_test.go）。
//
// 【为什么是「只有一个测试文件的包」】守卫检查的是 internal/ 下所有一级包之间的
// import 边，它不属于其中任何一个包：放进 knowledge 或 platform，等于让被检查者
// 自己给自己打分；而新建 internal/arch/ 目录又要多一个非测试文件。Go 允许一个
// 目录里只有 *_test.go（Go 1.26 实测：go test、go vet、go build ./... 都能正常
// 处理），所以 internal/arch_test.go 是这件事的最小落点。
//
// 【为什么用 go/parser 而不是 go list 或「编译后看 import」】两道理由：
//  1. 守卫读的是源码的静态事实，不该为了检查依赖方向去构建整个仓库。go/parser
//     只读文件，不碰数据库、不碰网络，断网机器与没有 Docker 的本机都能跑；
//  2. 重构期间源码可能暂时编译不过（正在挪文件、正在改签名），但这个守卫仍然
//     应该能回答「依赖方向有没有被破坏」——基于类型信息的实现做不到这一点。
//
// 【测试自身不依赖数据库/网络】：输入只有仓库里的 .go 文件 AST 和 go.mod 的
// module 行。没有任何 HTTP 调用、pgx、testcontainers。找不到仓库根时跳过并说明
// （见 guardRepoRoot），而不是猜一个目录去扫——扫错层的守卫会静默失效。
package internal

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// modulePath 是本仓库的 module 路径。守卫靠它区分「自家包」和第三方依赖，
	// 所以这里必须和 go.mod 里那一行一致（TestArchScannerSeesAllPackages 会核对，
	// 避免抄错后所有 import 都被当成第三方、守卫空转）。
	modulePath = "github.com/XiaoleC05/CongoRAG"
	// ginPath 是 HTTP 框架。internal 里只有 platform 允许提到它（见
	// ginAllowedImporters）。
	ginPath = "github.com/gin-gonic/gin"
	// netHTTPPath 是 net/http。内部包可以 import 它做出网客户端，但不该用它的
	// 状态码常量表达本服务的 API 语义（见 httpStatusAllowedImporters）。
	netHTTPPath = "net/http"
)

// allowedInternalEdges 是「internal 包依赖方向表」的可执行版本。
//
// 【为什么是显式边表，而不是层级数字】README 的「依赖方向」只写了三条原则
// （业务包不 import gin / 不知道 HTTP 状态码、消费方声明 port、两个装配根），
// ADR-001 引用的「代码架构设计 §2.1 方向表」在仓库里没有落成文件。把每条允许
// 的边单独写出来，失败信息才能指出「哪条边不请自来、该去哪解释」；换成层级
// 序号之后，「为什么允许 ctxmgr→llm」这种问题会被压成一个数字，新增边时也
// 没人需要解释理由。
//
// 未列出的边一律失败，包括将来新增的包（例如 R2 计划里的 internal/prompt）
// 第一次被 import 的时候：先来这张表登记、再同步 ADR，是刻意的摩擦。
//
// 约定：key 是 from（import 别人的包），value 是允许它 import 的 internal 一级包。
var allowedInternalEdges = map[string][]string{
	// agent 是最高层的业务编排：用 llm/平台能力、arxiv 的检索客户端、domain 的
	// 中性类型。它不 import knowledge/retrieval/conversation 的实现——需要的行为
	// 都由本包声明 port（见 internal/agent/port.go）。唯一的例外是流式事件类型，
	// 见 documentedDirectionExceptions。
	"agent": {"arxiv", "domain", "llm", "platform"},

	// arxiv 只是 arXiv API 的客户端适配器；用 platform 的错误 sentinel。
	"arxiv": {"platform"},

	// conversation 是聊天的业务层：上下文预算（ctxmgr）、证据落地（grounding）、
	// 模型调用（llm）、平台基础设施（platform）。
	"conversation": {"ctxmgr", "domain", "grounding", "llm", "platform", "prompt"},

	// ctxmgr 要按 llm.Tokenizer 计 token，依赖的是 llm 声明的接口（方向由消费方定）。
	"ctxmgr": {"domain", "llm", "prompt"},

	// knowledge 管知识库与文档；内容指纹要和检索用同一套切词规则，所以依赖 tokenize。
	"knowledge": {"domain", "platform", "tokenize"},

	// llm 是模型配置与调用适配器；它不依赖任何业务包。
	"llm": {"domain", "platform", "prompt"},

	// pdfparse 只把 PDF sidecar 的 JSON 解析成 domain 的块结构。
	"pdfparse": {"domain"},

	// recommend 的输入是 arxiv.Paper；platform 提供错误 sentinel。
	"recommend": {"arxiv", "platform"},

	// retrieval 是向量+词法检索：llm 提供 embedding/chat 入口，tokenize 提供
	// tsquery 切词，platform 提供 DB 与 sentinel。
	"retrieval": {"domain", "llm", "platform", "tokenize"},

	// visualize 是 CSV→图表的纯计算加 platform 的 sentinel。
	"visualize": {"platform"},

	// prompt 是提示词注册表：纯字符串与元数据，不依赖任何内部包（叶子）。
	// 需要提示词的包 import 它，它不 import 任何人——依赖方向永远是单向的。
	"prompt": {},

	// domain / platform / tokenize / grounding / testdb 没有出边：
	// domain 是中性类型叶；platform 是基础设施；tokenize/grounding 是纯算法；
	// testdb 只给 *_test.go 用（TestArchTestdbIsOnlyImportedFromTests 单独守）。
}

// directionException 是「结构上打破方向表、当前有意保留」的一条边。
//
// 【白名单不是偷懒】agent→conversation 不是随手写的：agent 的 Start/consumeEvents/
// Resume 全都要用 conversation.EventSink 与 conversation.Event 这对 SSE 边界类型
// （agent/usecase.go 共 11 处）。conversation 是会话流的产出方、agent 是运行流的
// 产出方，两条流最终都由 apps/api 的 sseSink 转发——共享的是流式协议的形状，
// 不是 conversation 的业务实现。把它们搬走是正解，但会牵动 conversation/agent/
// api 三处 20+ 个引用，放在 R3（异常/SSE 加固）同一轮做更稳。
//
// 每条白名单必须写清「为什么允许」和「怎么消掉」：没有还债路径的白名单会变成
// 永久豁免。并且白名单条目必须仍然存在——边被消掉而条目没删时测试会失败，
// 逼着清理（见 TestArchDependencyDirection 的「白名单过期」分支）。
type directionException struct {
	from string
	to   string
	why  string // 为什么现在允许
	how  string // 怎么消掉

	// allowedSelectors 把白名单从「整个包」收窄到「这个包里的哪些符号」。
	// 没有它的话，agent 以后 import conversation 取任何东西（业务用例、Repo、
	// 常量）都会因为这条边被放行——白名单就成了一个洞。显式列符号之后，
	// 只有 Event/EventSink 这两个流式边界类型可以通过。
	allowedSelectors []string
}

var documentedDirectionExceptions = []directionException{
	{
		from: "agent",
		to:   "conversation",
		why: "agent 的 Start / consumeEvents / Resume 都要用 conversation.EventSink 与 " +
			"conversation.Event 这对 SSE 边界类型（agent/usecase.go）。两者共享的是流式协议的" +
			"形状，不是 conversation 的业务实现。",
		how: "把 Event/EventSink 迁到 domain（或新增 internal/stream 叶包），api/sse.go、conversation、agent " +
			"三处换 import 后删掉本条，并同步 docs/adr/001-modular-monolith.md。",
		// 只认这两个类型：一旦 agent 开始用 conversation 的别的导出（比如
		// conversation.Usecase），说明它已经在依赖会话业务本身，必须失败。
		allowedSelectors: []string{"Event", "EventSink"},
	},
}

// ginAllowedImporters 是 internal 里允许 import gin 的包。
//
// 【为什么只有 platform 例外】README 的「依赖方向」写着「业务包不 import gin」，
// 而 platform 恰恰是 Gin 中间件（OriginCheck/Recovery/RequestID）、RFC 7807
// Problem 与 SSE 错误分类的实现处——它是 internal 与 HTTP 框架的唯一边界。
// 这条例外应当在 R1 的文档更新里写进 README/ADR，否则文档与实际互相打脸。
// 除 platform 外的任何 internal 包 import gin 都直接失败：业务包只认 platform
// 的 sentinel 与类型，不知道框架存在。
var ginAllowedImporters = map[string]string{
	"platform": "internal 与 gin 的唯一边界：中间件、Problem、SSE 错误分类都在这里；业务包只认 platform 的 sentinel。",
}

// httpStatusAllowedImporters 是允许使用 net/http 状态码常量的包。
//
// 【判据是「状态码语义」，不是「import 了 net/http」】arxiv/llm/pdfparse 是出网
// HTTP 客户端：它们必须看 resp.StatusCode 才知道上游成功没有——那是客户端语义，
// 不是本服务 API 的状态码。platform 是状态码的唯一映射点（Classify/中间件/
// Problem）。业务包把错误往上抛 sentinel，由 apps/api 交给 platform 映射成
// status+type，所以它们不该出现 http.StatusXxx。
var httpStatusAllowedImporters = map[string]string{
	"platform": "状态码的唯一映射点：Classify / 中间件 / RFC 7807 Problem。",
	"arxiv":    "出网客户端：判断 arXiv API 响应的 resp.StatusCode。",
	"llm":      "出网客户端：下载/校验 tiktoken 词表时判断 resp.StatusCode。",
	"pdfparse": "出网客户端：调用 PDF sidecar 时判断 resp.StatusCode。",
}

// httpStatusException 是「现在不合规、本轮不修」的 net/http 状态码使用点。
//
// 和方向白名单同一个纪律：每条必须写清收敛计划；条目不再命中时测试失败，
// 避免「修好了但白名单还挂着」。
type httpStatusException struct {
	pkg string
	why string
	how string

	// allowedFuncs 把例外收窄到「哪个函数里可以出现状态码」。只写包名的话，
	// conversation 以后在任何新函数里写 400/409 都会被放行；写清函数名之后，
	// 这个例外就只能覆盖它当初被批准的那一处。
	allowedFuncs []string
}

var documentedHTTPStatusExceptions = []httpStatusException{
	{
		pkg: "conversation",
		why: "eventErrorClass 直接返回 http.StatusBadRequest / http.StatusConflict，用来给 SSE " +
			"error 帧的 type 分类（conversation/usecase.go）。同一套映射在 platform.Classify " +
			"已经有一份，两处必须同步。",
		how: "R3 异常加固时让 eventErrorClass 只返回 type/sentinel（状态码交给 platform 统一映射），" +
			"然后删掉本条。",
		allowedFuncs: []string{"eventErrorClass"},
	},
}

// ────────────────────────────────────────────────────────────────
// 扫描器
// ────────────────────────────────────────────────────────────────

// parsedGoFile 是扫描结果里的一只文件：仓库内相对路径（报错用）+ AST。
//
// fset 跟着文件一起带：AST 节点只记偏移量，把偏移量翻成 file:line 必须用
// 解析它的那个 FileSet——守卫报错时的行号因此一定指向真实的源码位置。
type parsedGoFile struct {
	rel  string
	file *ast.File
	fset *token.FileSet
}

// guardRepoRoot 定位仓库根（go.mod 所在目录）。
//
// go test 会把工作目录切到包目录（这里是 <repo>/internal），所以不能假设 cwd
// 就是仓库根。用 runtime.Caller(0) 拿到本文件自己的绝对路径再上溯两层最稳；
// 内联/裁剪路径导致 Caller 不可用时，退化成从 cwd 逐级向上找 go.mod。
// 两者都失败就 t.Skip：宁可明确跳过，也不猜一个目录去扫——扫错层的守卫
// 会「零违规」地全绿，比没有守卫更危险。
func guardRepoRoot(t *testing.T) string {
	t.Helper()

	if _, file, _, ok := runtime.Caller(0); ok {
		// <repo>/internal/arch_test.go -> <repo>
		if root, found := repoRootFrom(filepath.Dir(filepath.Dir(file))); found {
			return root
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; dir = filepath.Dir(dir) {
			if root, found := repoRootFrom(dir); found {
				return root
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	t.Skip("无法定位仓库根（找不到含本 module 的 go.mod 与 internal/ 目录）：跳过依赖方向守卫。" +
		"这通常发生在把 arch_test.go 拷到别的仓库、或用非常规 -trimpath 构建测试时；" +
		"守卫不会因此猜测一个目录去扫描。")
	return ""
}

// repoRootFrom 判断 dir 是不是本仓库根。
//
// 【为什么还核对 go.mod 的 module 行】「有 internal 目录」不足以确认：monorepo
// 里任何一层都可能有个叫 internal 的目录，扫错层会让守卫静默失效。
func repoRootFrom(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || !strings.Contains(string(data), "module "+modulePath) {
		return "", false
	}
	info, err := os.Stat(filepath.Join(dir, "internal"))
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// listInternalPackages 返回 internal/ 下一级包目录名（排序后输出，报错稳定）。
//
// 只收目录；跳过 . 与 _ 开头的目录（Go 工具链同样忽略它们）。
func listInternalPackages(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatalf("读取 internal/ 目录失败：%v", err)
	}
	var pkgs []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		pkgs = append(pkgs, e.Name())
	}
	sort.Strings(pkgs)
	return pkgs
}

// parseProductionPackages 解析 internal/<pkg> 下所有非测试 .go 文件。
//
// 【只扫生产文件】*_test.go 可以 import gin/httptest/testdb 等测试专用依赖，
// 它们不进发布产物；守卫要守的是「产品代码有没有把框架/状态码/测试依赖带进
// internal」。测试文件自己的依赖由 go test 的编译约束管。
func parseProductionPackages(t *testing.T, fset *token.FileSet, root string) map[string][]parsedGoFile {
	t.Helper()
	out := map[string][]parsedGoFile{}
	for _, pkg := range listInternalPackages(t, root) {
		dir := filepath.Join(root, "internal", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读取 internal/%s 目录失败：%v", pkg, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			abs := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, abs, nil, 0)
			if err != nil {
				t.Fatalf("解析 internal/%s/%s 失败（守卫需要可解析的 Go 源码）：%v", pkg, name, err)
			}
			out[pkg] = append(out[pkg], parsedGoFile{
				rel:  filepath.ToSlash(filepath.Join("internal", pkg, name)),
				file: file,
				fset: fset,
			})
		}
	}
	return out
}

// mustUnquote 把 import spec 里的字符串字面量还原成路径。
func mustUnquote(t *testing.T, quoted string) string {
	t.Helper()
	path, err := strconv.Unquote(quoted)
	if err != nil {
		t.Fatalf("import 路径 %s 不是合法字符串字面量：%v", quoted, err)
	}
	return path
}

// internalTarget 把本仓库的 import 路径折成 internal 一级包名。
//
// 例：github.com/XiaoleC05/CongoRAG/internal/agent -> ("agent", true)
//
// 返回 false 表示这条 import 不是本仓库 internal 包（第三方依赖、标准库）。
func internalTarget(t *testing.T, importPath string) (string, bool) {
	t.Helper()
	rest, ok := strings.CutPrefix(importPath, modulePath+"/internal/")
	if !ok {
		return "", false
	}
	target, sub, nested := strings.Cut(rest, "/")
	if nested {
		// internal 目前只有一级包；出现二级 import 说明有人把包拆出了子目录，
		// 依赖方向表还没覆盖这种情况。直接报出来，而不是悄悄折成一级包。
		t.Fatalf("发现二级 internal 包 import：%s（%s）。依赖方向表只覆盖 internal/ 下的一级包，"+
			"请先决定它归到哪一层再更新本文档。", importPath, sub)
	}
	return target, true
}

// httpLocalNames 返回某个文件里 net/http 的本地名字。
//
// 默认是 "http"，但 import 别名（nethttp "net/http"）或 dot-import 都要认：
// 点号 import 无法静态知道哪个标识符来自 net/http，测试直接判失败，避免
// 用 dot-import 绕过状态码守卫。
func httpLocalNames(t *testing.T, parsed parsedGoFile) (names []string, dotImported bool) {
	t.Helper()
	for _, spec := range parsed.file.Imports {
		if mustUnquote(t, spec.Path.Value) != netHTTPPath {
			continue
		}
		if spec.Name == nil {
			names = append(names, "http")
			continue
		}
		if spec.Name.Name == "." {
			dotImported = true
			continue
		}
		names = append(names, spec.Name.Name)
	}
	return names, dotImported
}

// selectorRef 是「某个 import 的本地名在文件里被用于哪个选择器」的一条记录。
type selectorRef struct {
	name string
	pos  token.Position
}

// selectorRefs 返回文件里 localName.X 形态的全部选择器引用。
//
// 用途是把方向白名单收窄到符号级：agent import conversation 本身可以被批准，
// 但「用了 conversation.Event」和「用了 conversation.Usecase」是两件事——
// 前者是两条流共享的协议形状，后者是依赖会话业务实现。
func selectorRefs(parsed parsedGoFile, localName string) []selectorRef {
	var refs []selectorRef
	ast.Inspect(parsed.file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != localName {
			return true
		}
		refs = append(refs, selectorRef{name: sel.Sel.Name, pos: parsed.fset.Position(sel.Pos())})
		return true
	})
	return refs
}

// httpStatusUsage 是一条状态码使用记录：位置 + 它所在（最内层）的函数名。
//
// 函数名是收窄 HTTP 状态码白名单用的：conversation 的例外只覆盖
// eventErrorClass 这一处；写成包级例外的话，这个包以后在任何新函数里写
// 400/409 都会被放行，例外会慢慢变成整包豁免。
type httpStatusUsage struct {
	pos token.Position
	fn  string // 包级 var/const 等不在函数内的引用记作 ""
}

// httpStatusUses 在单个文件里找 http.StatusXxx 形态的选择表达式，并标出
// 它落在哪个函数里。
//
// 【为什么只看 SelectorExpr 的文本形状】守卫不需要类型信息：这个仓库里
// net/http 的本地名不会和局部变量重名（真重名时极少数误报也只会让人来看一眼，
// 代价远低于为了让守卫用 go/types 而强依赖整个仓库可编译）。
func httpStatusUses(t *testing.T, parsed parsedGoFile, localNames []string) []httpStatusUsage {
	t.Helper()
	if len(localNames) == 0 {
		return nil
	}
	type funcRange struct {
		name       string
		start, end token.Pos
	}
	var funcs []funcRange
	for _, decl := range parsed.file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
			funcs = append(funcs, funcRange{name: fd.Name.Name, start: fd.Pos(), end: fd.End()})
		}
	}
	funcAt := func(pos token.Pos) string {
		for _, fn := range funcs {
			if fn.start <= pos && pos <= fn.end {
				return fn.name
			}
		}
		return ""
	}

	var out []httpStatusUsage
	ast.Inspect(parsed.file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Status") {
			return true
		}
		for _, name := range localNames {
			if ident.Name == name {
				out = append(out, httpStatusUsage{pos: parsed.fset.Position(sel.Pos()), fn: funcAt(sel.Pos())})
				break
			}
		}
		return true
	})
	return out
}

// directionExceptionFor 按 (from, to) 查方向白名单。
func directionExceptionFor(from, to string) (directionException, bool) {
	for _, ex := range documentedDirectionExceptions {
		if ex.from == from && ex.to == to {
			return ex, true
		}
	}
	return directionException{}, false
}

// httpStatusExceptionFor 按包名查状态码白名单。
func httpStatusExceptionFor(pkg string) (httpStatusException, bool) {
	for _, ex := range documentedHTTPStatusExceptions {
		if ex.pkg == pkg {
			return ex, true
		}
	}
	return httpStatusException{}, false
}

// displayFunc 把「不在任何函数里」的占位空串渲染成可读文案。
func displayFunc(name string) string {
	if name == "" {
		return "包级变量/常量"
	}
	return name
}

// ────────────────────────────────────────────────────────────────
// 守卫 1：包依赖方向
// ────────────────────────────────────────────────────────────────

// TestArchDependencyDirection 断言 internal 一级包之间只存在方向表允许的 import 边。
//
// 失败时逐条列出：哪个包 import 了哪个包、发生在哪个文件哪一行、为什么不允许、
// 以及「有意的架构调整该去改哪里」。
func TestArchDependencyDirection(t *testing.T) {
	root := guardRepoRoot(t)
	fset := token.NewFileSet()
	pkgs := parseProductionPackages(t, fset, root)

	type edgeKey struct{ from, to string }
	actual := map[edgeKey][]string{} // 值：repo 相对的 file:line
	var violations []string

	for from, files := range pkgs {
		for _, parsed := range files {
			for _, spec := range parsed.file.Imports {
				importPath := mustUnquote(t, spec.Path.Value)

				// 分层：internal 是库层，apps 是进程装配层。反过来 import
				// 等于让库层依赖某个进程的接线方式，worker/api 将无法独立演进。
				if strings.HasPrefix(importPath, modulePath+"/apps/") {
					violations = append(violations, fmt.Sprintf(
						"分层违规：%s 的 %s:%d import 了进程装配层的 %s。\n"+
							"  internal/ 是库层、apps/ 是进程装配层，方向只能 apps→internal；"+
							"库层需要进程给的东西，应该在装配根里注入，而不是反向 import。",
						from, parsed.rel, fset.Position(spec.Pos()).Line, importPath))
					continue
				}

				to, ok := internalTarget(t, importPath)
				if !ok {
					continue
				}
				pos := fset.Position(spec.Pos())
				actual[edgeKey{from, to}] = append(actual[edgeKey{from, to}],
					fmt.Sprintf("%s:%d", parsed.rel, pos.Line))

				// 白名单边不是「整包放行」。这条 import 实际用到的符号必须落在
				// allowedSelectors 里，否则白名单会宽到可以把 conversation 的
				// 任何导出都搬进 agent。
				if ex, ok := directionExceptionFor(from, to); ok {
					localName := to
					if spec.Name != nil {
						localName = spec.Name.Name
					}
					// 空白 import 与 dot-import 都要拒绝：前者说明不了用途，后者会让
					// selectorRefs 一个引用都匹配不到（conversation.Event 变成裸 Event），
					// 白名单于是静默通过——恰好是这道守卫要防的「白名单变洞」。
					if localName == "_" || localName == "." {
						violations = append(violations, fmt.Sprintf(
							"白名单越界：internal/%s 的 %s:%d 用空白/dot import 引入了 internal/%s。\n"+
								"  白名单只批准 [%s] 这些符号；这两种 import 都让「用了哪些符号」无法静态审计，"+
								"请改成显式 import 或删掉。",
							from, parsed.rel, pos.Line, to, strings.Join(ex.allowedSelectors, ", ")))
						continue
					}
					for _, ref := range selectorRefs(parsed, localName) {
						if containsString(ex.allowedSelectors, ref.name) {
							continue
						}
						violations = append(violations, fmt.Sprintf(
							"白名单越界：internal/%s 的 %s:%d 使用了 %s.%s。\n"+
								"  这条白名单边只批准 [%s]（见 documentedDirectionExceptions）；用了别的符号说明"+
								"依赖已经超出「共享流式类型」的范围：要么把依赖拆掉，要么先更新 ADR 与本表再放行。",
							from, parsed.rel, ref.pos.Line, to, ref.name, strings.Join(ex.allowedSelectors, ", ")))
					}
				}
			}
		}
	}

	usedExceptions := map[edgeKey]bool{}
	for key, places := range actual {
		if containsString(allowedInternalEdges[key.from], key.to) {
			continue
		}
		if _, ok := directionExceptionFor(key.from, key.to); ok {
			usedExceptions[key] = true
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"依赖方向违规：internal/%s import 了 internal/%s（%s）。\n"+
				"  allowedInternalEdges 里 %s 允许的是 [%s]，没有 %s。\n"+
				"  如果这是一次有意的架构调整：先把这条边写进 docs/adr/001-modular-monolith.md 的依赖方向表、"+
				"再更新本测试的 allowedInternalEdges，并在 PR 里说明理由；不要直接把它加进白名单。",
			key.from, key.to, strings.Join(places, "、"),
			key.from, strings.Join(allowedInternalEdges[key.from], ", "), key.to))
	}

	// 【白名单必须仍是真实存在的边】消掉一条边却忘了删白名单，守卫的覆盖面
	// 会被一条「不存在的例外」慢慢撑大；过期条目本身要触发失败。
	for _, ex := range documentedDirectionExceptions {
		if !usedExceptions[edgeKey{ex.from, ex.to}] {
			violations = append(violations, fmt.Sprintf(
				"白名单过期：internal/%s -> internal/%s 这条 import 边已经不存在了。\n"+
					"  说明它已经被消掉——请删除 documentedDirectionExceptions 里的条目"+
					"（原收敛计划：%s），并同步 ADR。",
				ex.from, ex.to, ex.how))
		}
	}

	reportViolations(t, "依赖方向", violations)
}

// ────────────────────────────────────────────────────────────────
// 守卫 2：internal 包不得 import gin（platform 例外）
// ────────────────────────────────────────────────────────────────

// TestArchInternalPackagesDoNotImportGin 断言除 platform 外，internal 一级包的
// 生产代码不 import gin。测试文件不在扫描范围（见 parseProductionPackages）。
func TestArchInternalPackagesDoNotImportGin(t *testing.T) {
	root := guardRepoRoot(t)
	fset := token.NewFileSet()
	pkgs := parseProductionPackages(t, fset, root)

	var violations []string
	for pkg, files := range pkgs {
		if _, allowed := ginAllowedImporters[pkg]; allowed {
			continue
		}
		for _, parsed := range files {
			for _, spec := range parsed.file.Imports {
				if mustUnquote(t, spec.Path.Value) != ginPath {
					continue
				}
				violations = append(violations, fmt.Sprintf(
					"HTTP 框架泄漏：internal/%s 的 %s:%d import 了 gin。\n"+
						"  internal 里只有 platform 允许接触 gin（它是中间件/Problem/SSE 错误分类的适配层）；"+
						"业务包与算法包只返回 sentinel error，由 apps/api 映射成 HTTP 响应。",
					pkg, parsed.rel, fset.Position(spec.Pos()).Line))
			}
		}
	}

	reportViolations(t, "gin 依赖", violations)
}

// ────────────────────────────────────────────────────────────────
// 守卫 3：业务包不得知道 net/http 状态码
// ────────────────────────────────────────────────────────────────

// TestArchBusinessPackagesDoNotKnowHTTPStatus 断言 internal 一级包的生产代码里，
// 除 platform 与出网客户端（arxiv/llm/pdfparse）外，不出现 http.StatusXxx。
//
// 【为什么这条比「禁止 import net/http」更准确】arxiv/llm/pdfparse 必须 import
// net/http 才能发出网请求，也当然要看 resp.StatusCode；被判违规的是「用状态码
// 表达本服务 API 语义」的写法（典型：给 SSE 帧或返回值挑 400/409）。
func TestArchBusinessPackagesDoNotKnowHTTPStatus(t *testing.T) {
	root := guardRepoRoot(t)
	fset := token.NewFileSet()
	pkgs := parseProductionPackages(t, fset, root)

	usedExceptions := map[string]bool{}
	var violations []string
	for pkg, files := range pkgs {
		if _, allowed := httpStatusAllowedImporters[pkg]; allowed {
			continue
		}
		for _, parsed := range files {
			localNames, dotImported := httpLocalNames(t, parsed)
			if dotImported {
				violations = append(violations, fmt.Sprintf(
					"无法审计的状态码引用：internal/%s 的 %s 用 dot-import 引入了 net/http。\n"+
						"  点号 import 让静态分析无法区分哪些标识符来自 net/http；请改回显式 import，再重新审视状态码用法。",
					pkg, parsed.rel))
				continue
			}
			uses := httpStatusUses(t, parsed, localNames)
			if len(uses) == 0 {
				continue
			}
			// 例外不是「整个包都可以知道状态码」：只允许在 allowedFuncs 列出的
			// 函数里出现，别的新函数再写 400/409 一样要失败。
			if ex, ok := httpStatusExceptionFor(pkg); ok {
				usedExceptions[pkg] = true
				for _, u := range uses {
					if containsString(ex.allowedFuncs, u.fn) {
						continue
					}
					violations = append(violations, fmt.Sprintf(
						"白名单越界：internal/%s 的 %s:%d（函数 %s）使用了 net/http 状态码常量。\n"+
							"  这条例外只覆盖 [%s]；其它位置要么走 platform.Classify，要么先写明收敛计划再放行。",
						pkg, parsed.rel, u.pos.Line, displayFunc(u.fn), strings.Join(ex.allowedFuncs, ", ")))
				}
				continue
			}
			for _, u := range uses {
				violations = append(violations, fmt.Sprintf(
					"状态码泄漏：internal/%s 的 %s:%d（函数 %s）使用了 net/http 状态码常量。\n"+
						"  业务包不应该知道 HTTP 状态码：错误用 platform sentinel 往上透传，"+
						"映射只发生在 platform.Classify / apps/api。\n"+
						"  出网 HTTP 客户端（arxiv/llm/pdfparse 检查上游 resp.StatusCode）与 platform 不在此列。",
					pkg, parsed.rel, u.pos.Line, displayFunc(u.fn)))
			}
		}
	}

	// 过期例外同样会让守卫变宽：修好后必须删条目。
	for _, ex := range documentedHTTPStatusExceptions {
		if !usedExceptions[ex.pkg] {
			violations = append(violations, fmt.Sprintf(
				"白名单过期：internal/%s 已经不再使用 net/http 状态码常量。\n"+
					"  请删除 documentedHTTPStatusExceptions 里的条目（原收敛计划：%s）。",
				ex.pkg, ex.how))
		}
	}

	reportViolations(t, "HTTP 状态码", violations)
}

// ────────────────────────────────────────────────────────────────
// 守卫 4：internal/testdb 只能被测试文件 import
// ────────────────────────────────────────────────────────────────

// TestArchTestdbIsOnlyImportedFromTests 断言没有生产文件 import internal/testdb。
//
// testdb 拉的是 testcontainers + 两套迁移，只应在 *_test.go 里出现。一旦某个
// 生产文件 import 它，api/worker 二进制的依赖清单会把整条测试基础设施链拖进来，
// 而这个问题在编译期完全看不出来（testdb 的所有导出都合法）。
func TestArchTestdbIsOnlyImportedFromTests(t *testing.T) {
	root := guardRepoRoot(t)
	fset := token.NewFileSet()

	var violations []string
	for _, top := range []string{"internal", "apps"} {
		dir := filepath.Join(root, top)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil // 测试文件允许
			}
			file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				t.Fatalf("解析 %s 失败：%v", path, perr)
			}
			for _, spec := range file.Imports {
				importPath, uerr := strconv.Unquote(spec.Path.Value)
				if uerr != nil || importPath != modulePath+"/internal/testdb" {
					continue
				}
				rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
				violations = append(violations, fmt.Sprintf(
					"测试依赖泄漏：生产文件 %s:%d import 了 internal/testdb。\n"+
						"  testdb 拉 testcontainers + 迁移，只允许 *_test.go 使用；生产代码需要空库/假实现时用接口注入。",
					rel, fset.Position(spec.Pos()).Line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s/ 失败：%v", top, err)
		}
	}

	reportViolations(t, "testdb 隔离", violations)
}

// ────────────────────────────────────────────────────────────────
// 守卫自检
// ────────────────────────────────────────────────────────────────

// TestArchScannerSeesAllPackages 是扫描器的自检。
//
// 【为什么必须有这条】守卫类测试最危险的失效方式不是「报错」，而是扫描器
// 自己坏了（定位目录失败、前缀写错、ReadDir 行为变化）却「零违规」地全绿。
// 这里断言：至少发现 8 个一级包、README 列出的八个业务包都在、每个包至少
// 一个生产文件——任何一项不成立，另外三条守卫就不可信。
func TestArchScannerSeesAllPackages(t *testing.T) {
	root := guardRepoRoot(t)
	pkgs := listInternalPackages(t, root)
	if len(pkgs) < 8 {
		t.Fatalf("扫描器只发现 %d 个 internal 一级包（%v）：少于八个业务包，"+
			"说明定位仓库根或读目录出了问题，其余守卫会空转。", len(pkgs), pkgs)
	}
	present := map[string]bool{}
	for _, p := range pkgs {
		present[p] = true
	}
	// README「依赖方向」点名的那八个业务包；缺一个都说明扫描范围不对。
	for _, want := range []string{"agent", "conversation", "ctxmgr", "domain", "knowledge", "llm", "platform", "retrieval"} {
		if !present[want] {
			t.Errorf("扫描器没有发现业务包 internal/%s（发现的包：%v）：守卫不可信。", want, pkgs)
		}
	}

	fset := token.NewFileSet()
	parsed := parseProductionPackages(t, fset, root)
	for _, p := range pkgs {
		if len(parsed[p]) == 0 {
			t.Errorf("internal/%s 没有扫描到任何生产 .go 文件：要么它只有测试文件，"+
				"要么扫描器漏了这个目录。", p)
		}
	}

	// go.mod 的 module 行必须与常量一致，否则 internalTarget 会把所有自家
	// import 当成第三方依赖（守卫同样会全绿）。这条自检把「常量抄错」的窗口关掉。
	modData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("读取 go.mod 失败：%v", err)
	}
	if !strings.Contains(string(modData), "module "+modulePath) {
		t.Fatalf("go.mod 的 module 行与 arch_test.go 的 modulePath=%q 不一致：internal 包 import 边会被全部漏判。", modulePath)
	}
}

// ────────────────────────────────────────────────────────────────
// 工具函数
// ────────────────────────────────────────────────────────────────

// reportViolations 统一输出：排序后再报，保证同一次违规的失败信息可复现。
func reportViolations(t *testing.T, rule string, violations []string) {
	t.Helper()
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	t.Errorf("%s 守卫失败，共 %d 条：\n\n%s", rule, len(violations), strings.Join(violations, "\n\n"))
}

// containsString 判断切片里有没有 s（边表都很小，线性查找足够，不引入额外依赖）。
func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
