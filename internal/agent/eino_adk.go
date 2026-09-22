// eino_adk.go 是 agent 包唯一 import cloudwego/eino/adk（以及 Eino 的
// schema/model/tool/compose 子包）的文件。和 internal/llm/eino.go 是同一个
// 隔离原则的第二个落地点——ADK 的工具绑定/ReAct 循环需要的是 Eino 原生的
// model.ToolCallingChatModel + tool.BaseTool,不是 llm 包自己的 ChatModel
// 包装类型,所以这条隔离线必须在 agent 包里单独画一条,不能指望 llm 包
// 把 ADK 也藏住（见 internal/llm/port.go 的 Registry 注释）。
//
// 本文件对外只暴露 adkEvent（本包自己的中性事件）和 runAgent 函数——
// usecase.go 从未见过 schema.Message 或 adk.AgentEvent。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	einochatmodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	jsonschemalib "github.com/eino-contrib/jsonschema"

	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// maxIterations 是 ADK ChatModelAgent 一次执行里"模型生成→工具调用"
// 这个循环的最大轮数——防止模型陷入无限工具调用循环时进程被拖死。
// ADK 自己的默认值也是 20（config.go 的文档注释),这里显式写出来,
// 不依赖对方的默认值不变。
const maxIterations = 20

// resumeTurn 是恢复时重建历史要用的一轮工具调用。
//
// 【为什么要重建历史，而不是让 Eino 接着跑】ADR-006 决定了不接 Eino 的
// CheckPointStore，所以编排 runtime 的状态在崩溃时就没了——重启后没有任何
// 东西能让那张 ReAct 图"接着上次"往下走。能重建的只有**业务事实**：
// agent_run_steps 里那些已经完成的工具调用（工具名、参数、结果）。
// 把它们拼成"这个会话已经发生过这些事"的历史交给模型，模型自然会往下做
// 而不是从头再来——这就是 §9.3 说的"从下一个 Step 重放"。
//
// 【没有 assistant 的正文】agent_run_steps 不存模型每一轮的文本输出
// （只存 type=llm 这一行本身）。而 ReAct 里真正驱动下一步的是工具调用与
// 结果，所以重建重点是这两样——正文缺失不影响模型继续，它看到的是
// "这些工具已经调过、结果如下"。
type resumeTurn struct {
	// Seq 是这一步在 run 里的原始序号（agent_run_steps.seq）。
	//
	// 【为什么历史要按它排序，而不是按"什么时候被重放的"】一轮里模型可以
	// 并行请求多个工具，而它们的**结果**是乱序到达的（本包的注释自己就假设
	// 了这一点）。崩溃现场于是可能长成"seq3 已完成、seq2 还没回来"，恢复拼
	// 给模型的历史如果按"先完成的先放"，模型看到的调用顺序就与真实发生的
	// 相反——不报错、不崩，但它对"已经发生过什么"的理解是错的。
	Seq        int
	ToolName   string
	ToolArgs   json.RawMessage
	ToolResult json.RawMessage
}

// adkEvent 是 Eino ADK 事件流的中性投影,usecase.go 消费的是这个类型,
// 从未导入任何 Eino 包。
type adkEvent struct {
	kind       string // token | tool_call | tool_result | usage | error | done
	text       string
	toolCallID string
	toolName   string
	toolArgs   json.RawMessage
	toolResult json.RawMessage
	// usage 是这一轮模型调用的 token 用量（issue #47）。
	//
	// 【为什么它要走事件通道】ADK 路径自己构造 Eino 原生模型，刻意不经过
	// llm 包的适配器（见文件头注释），所以适配器那套自动记账覆盖不到它。
	// 代价由调用方承担：绕开封装，也要自己把观测数据报出去。
	usage *schema.TokenUsage
	err   error
}

const (
	adkEventToken      = "token"
	adkEventToolCall   = "tool_call"
	adkEventToolResult = "tool_result"
	adkEventError      = "error"
	adkEventDone       = "done"
	adkEventUsage      = "usage"
)

// toolAdapter 把本包的 Tool 接口包成 Eino 的 tool.InvokableTool——
// Info() 负责把 ToolSpec.Schema（标准 JSON Schema 文本)解析成 Eino
// 认识的 schema.ParamsOneOf,InvokableRun() 负责把 Eino 的"字符串参数/
// 字符串结果"协议转换成本包 Tool.Invoke 的 json.RawMessage 协议。
type toolAdapter struct {
	t Tool
}

var _ tool.InvokableTool = (*toolAdapter)(nil)

func (a *toolAdapter) Info(ctx context.Context) (*schema.ToolInfo, error) {
	spec := a.t.Spec()
	var js jsonschemalib.Schema
	if err := json.Unmarshal(spec.Schema, &js); err != nil {
		return nil, fmt.Errorf("parse json schema for tool %s: %w", spec.Name, err)
	}
	return &schema.ToolInfo{
		Name:        spec.Name,
		Desc:        spec.Description,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&js),
	}, nil
}

// toolError 是"这次工具调用失败了"交回模型时的结果形状——和工具正常的
// 返回值一样是个 JSON 对象，模型从 error 字段读到原因后可以改参数重试。
type toolError struct {
	Error string `json:"error"`
}

// maxToolResultBytes 是单次工具结果交给模型前的字节上限。
//
// 【为什么这条路径上必须有它】普通聊天那条走 ctxmgr.Build，系统提示、历史、
// chunk 都在那里按 token 裁；Agent 这条是一个由 Eino 驱动的 ReAct 循环，
// 没有一个静态的"组装点"可以交给 ctxmgr，工具结果原样进历史。而结果可以
// 很大：conversation_search 在 500 条消息里返回**每条消息的完整正文**
// （internal/conversation/usecase.go 的 searchMessagesLimit），几百 KB
// 塞给模型必然超出上下文窗口。
//
// 更麻烦的是报错的方向：上游返回的错误会经 agentEventError 落到
// upstream_llm_error，而前端按这个 type 提示用户"检查 API Key 和配额"
// ——排查方向从第一句话起就是错的。这正是 issue #34 修掉的那类误报，只是
// 换到了 Agent 这条路径上（issue #109）。所以在工具边界上主动截断。
//
// 【16 KiB 是怎么定的】一次检索类工具结果约合 2–4k token，对任何还有余量
// 的模型都塞得下；再大就该让模型换更精确的查询，而不是把整个历史喂给它。
// 20 轮（maxIterations）全打满也只有 320 KiB 的量级，仍在常见窗口之内。
const maxToolResultBytes = 16 * 1024

// truncatedToolResult 是超限的工具结果交回模型时的形状。
//
// 【为什么包一层对象，而不是把 JSON 剪短】tool_result 那一列是 jsonb
// （migrations/0005_agents.up.sql:78），写一份被拦腰截断的文本进去会让整行
// UPDATE 失败，而那个失败只记一行日志——轨迹静默缺一行。包一层既保证
// 结果是合法 JSON，又让模型从 truncated/notice 两个字段**明确知道**
// "后面还有内容被拿掉了"（issue #109 要的正是"不要静默丢弃"）。
type truncatedToolResult struct {
	Truncated bool   `json:"truncated"`
	Notice    string `json:"notice"`
	// Preview 是被保留下来的那一段原文（它本身是这段结果的 JSON 文本，
	// 在这里当字符串放，不要求它自己可解析——刀口落在哪就是哪）。
	Preview string `json:"preview"`
}

// capToolResult 把一份工具结果截到 maxToolResultBytes 以内——返回值的长度
// 是**硬上限**，不是"接近"。
//
// 【为什么是循环而不是切一刀】保留下来的那一段是原文（JSON 文本），塞进
// truncatedToolResult.Preview 这个字符串字段时每个引号都要转义成 \"，
// 控制字符更夸张——切 16 KiB 原文有可能编出 96 KiB 的 JSON。所以要一轮轮
// 缩，直到序列化结果本身也在上限之内。每次至少缩 1 字节，必然终止。
//
// 【为什么按 rune 回退】直接切字节会把一个多字节字符劈成两半，
// json.Marshal 之后模型读到的是 U+FFFD 乱码，还看不出那是截断造成的。
func capToolResult(result []byte) []byte {
	if len(result) <= maxToolResultBytes {
		return result
	}

	head := result[:maxToolResultBytes]
	for len(head) > 0 {
		if !utf8.Valid(head) {
			head = head[:len(head)-1]
			continue
		}
		capped, err := json.Marshal(truncatedToolResult{
			Truncated: true,
			Notice: fmt.Sprintf(
				"结果被截断：只保留了前 %d 字节（原始 %d 字节）。请换更精确的参数重试，或者分几次取。",
				len(head), len(result)),
			Preview: string(head),
		})
		if err == nil && len(capped) <= maxToolResultBytes {
			return capped
		}
		head = head[:len(head)-len(head)/8-1]
	}

	// 原文全是控制字符之类的极端情况——宁可只交回一句通知，也不返回一份
	// 超限的结果。
	return []byte(`{"truncated":true,"notice":"结果过长，已截断。请换更精确的参数重试。"}`)
}

func (a *toolAdapter) InvokableRun(ctx context.Context, argsJSON string, opts ...tool.Option) (string, error) {
	result, err := a.t.Invoke(ctx, json.RawMessage(argsJSON))
	if err == nil {
		// 【截断必须发生在这里】这是工具结果回到 Eino 消息历史的唯一入口，
		// 也就是模型下一次生成会读到的那个字符串。放在别处（比如消费事件
		// 的地方）只能截到落库的那一份，模型仍然会收到全量。
		return string(capToolResult(result)), nil
	}

	// 参数级的失败（除零、非法 UUID、JSON 解析不了）当成一次普通的 tool
	// result 交回模型，不能作为 Go error 返回：Eino 的 compose ToolsNode
	// 把 InvokableTool 返回的错误当致命错误——不写 tool message，直接让
	// 整张 ReAct 图失败。模型永远收不到 tool_result，也就没有机会改参数
	// 重试，一次 b=0 的除法就能打死整轮 run（issue #15）。
	//
	// 只降级 platform.ErrInvalid：检索/查库真的挂掉是基础设施故障，重试
	// 也没有意义，应该让整轮按上游故障结束；ctx 已取消时同理，这次运行
	// 正在被拆掉，不该再让模型多跑一轮。
	if ctx.Err() != nil || !errors.Is(err, platform.ErrInvalid) {
		return "", err
	}

	payload, marshalErr := json.Marshal(toolError{Error: err.Error()})
	if marshalErr != nil {
		return "", err
	}
	return string(payload), nil
}

// runAgent 构造一个真实的 ADK ChatModelAgent + Runner,跑一次查询,
// 把事件归一化成 <-chan adkEvent 返回。
//
// 【为什么返回 channel 而不是 AsyncIterator】usecase.go 不认识 Eino 的
// AsyncIterator 类型——channel 是 Go 的原生同步原语,是这条边界唯一
// 应该出现的类型。
func runAgent(
	ctx context.Context,
	registry llm.Registry,
	chatModelID string,
	ag *Agent,
	tools []Tool,
	input string,
	priorTurns []resumeTurn,
) (<-chan adkEvent, error) {
	baseURL, apiKey, modelName, err := registry.ResolveChatEndpoint(ctx, chatModelID)
	if err != nil {
		return nil, fmt.Errorf("resolve chat endpoint: %w", err)
	}

	chatModel, err := einochatmodel.NewChatModel(ctx, &einochatmodel.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   modelName,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: init chat model %s: %v", platform.ErrUpstream, chatModelID, err)
	}

	einoTools := make([]tool.BaseTool, len(tools))
	for i, t := range tools {
		einoTools[i] = &toolAdapter{t: t}
	}

	chatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        ag.Name,
		Description: ag.Description,
		Instruction: ag.Instruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: einoTools},
		},
		MaxIterations: maxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("build chat model agent: %w", err)
	}

	// 【故意不传 CheckPointStore】ADR-006 的决定：Eino 那层的 checkpoint
	// 只在中断点写，进程被 KILL 时一个字节都不会落，接上它对崩溃恢复
	// 贡献为零。恢复的唯一权威是业务层（agent_runs + agent_run_steps）。
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true})

	// 【两条路径分开走，不合并】全新运行继续用 Query——它与 ADK 内部
	// 构造首条用户消息的方式完全一致。带历史时走 Run，因为只有它接受
	// 一批现成的消息。合并成一条 Run(msgs) 看起来更整齐，但会让全新运行
	// 这条主路径也依赖"我们自己拼的首条消息和 ADK 拼的一样"这个假设。
	var iter *adk.AsyncIterator[*adk.AgentEvent]
	if len(priorTurns) == 0 {
		iter = runner.Query(ctx, input)
	} else {
		msgs := make([]*schema.Message, 0, 1+2*len(priorTurns))
		msgs = append(msgs, schema.UserMessage(input))
		for i, turn := range priorTurns {
			// 【工具调用 id 是这里编的】agent_run_steps 不存 Eino 给的
			// toolCallID（那是编排层的东西），但 assistant 消息里的
			// ToolCalls 与随后的 tool 消息必须靠 id 配对。编一个稳定的
			// id 就够了——模型读到的是一个"叫什么名字、结果是什么"的
			// 历史，而不是要拿它去查什么东西。
			callID := fmt.Sprintf("resume_%d", i)
			msgs = append(msgs,
				schema.AssistantMessage("", []schema.ToolCall{{
					ID: callID,
					Function: schema.FunctionCall{
						Name:      turn.ToolName,
						Arguments: string(turn.ToolArgs),
					},
				}}),
				// 【这里也过一遍上限】新写进去的结果在工具边界上已经截过了
				// （capToolResult 在 InvokableRun 里），但库里可能还躺着加
				// 这个上限之前存下来的超大行——恢复一条老 run 时它们会原样
				// 进历史，把预算一次性撑爆。这一层是幂等的：已经截过的值
				// 远低于上限，原样返回。
				schema.ToolMessage(string(capToolResult(turn.ToolResult)), callID),
			)
		}
		iter = runner.Run(ctx, msgs)
	}

	// 带一点缓冲：终结的 done/error 事件不该在接收方刚好慢一拍时卡住
	// 生产者，取消也被这一层吸收掉。
	out := make(chan adkEvent, 16)
	go drainIterator(ctx, iter, out)
	return out, nil
}

// sendEvent 往 out 发一个事件，ctx 取消（客户端断开、消费端提前退出）时
// 返回 false 让调用方收摊。
//
// 【为什么每一处发送都必须 select ctx.Done()】out 的接收方是
// usecase.consumeEvents，它在第一次 emit 失败时就直接返回——客户端断开
// 时正是如此，此后没有任何人再读 out。无条件 send 会让这个 goroutine
// 永久阻塞在一次 channel send 上，连带 Eino 的 iterator 一起泄漏，客户端
// 每中断一次泄漏一套，且没有任何回收路径（issue #35）。
func sendEvent(ctx context.Context, out chan<- adkEvent, ev adkEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func drainIterator(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], out chan<- adkEvent) {
	defer close(out)

	for {
		event, ok := iter.Next()
		if !ok {
			sendEvent(ctx, out, adkEvent{kind: adkEventDone})
			return
		}
		if event.Err != nil {
			sendEvent(ctx, out, adkEvent{kind: adkEventError, err: event.Err})
			return
		}

		mv := event.Output.MessageOutput
		if mv == nil {
			continue
		}

		switch mv.Role {
		case schema.Assistant:
			if err := emitAssistantEvents(ctx, mv, out); err != nil {
				sendEvent(ctx, out, adkEvent{kind: adkEventError, err: err})
				return
			}
		case schema.Tool:
			msg, err := mv.GetMessage()
			if err != nil {
				sendEvent(ctx, out, adkEvent{kind: adkEventError, err: fmt.Errorf("read tool result message: %w", err)})
				return
			}
			if !sendEvent(ctx, out, adkEvent{
				kind: adkEventToolResult, toolCallID: msg.ToolCallID,
				toolName: mv.ToolName, toolResult: json.RawMessage(msg.Content),
			}) {
				return
			}
		}
	}
}

// emitAssistantEvents 处理模型自己生成的这一轮输出——可能是纯文本回答
// （流式,逐块发 token 事件),也可能是工具调用请求（ToolCalls 非空,
// 一次性发,不逐块——模型决定调哪个工具这件事本身不是"渐进呈现"
// 有意义的内容,不需要流式）。
//
// 【手动排空流,不直接调 mv.GetMessage()】GetMessage 内部会自己消费
// MessageStream 并 concat——如果直接调用,就拿不到中间的每一块增量,
// 前端会等到整段话生成完才看到文字,失去流式的意义。这里手动排空、
// 边收边发 token,收完再自己 concat 一次判断有没有 ToolCalls。
//
// 发送被 ctx 取消打断时返回 ctx.Err()：模型流要先关掉（后面的块没人要了），
// drainIterator 收到这个错误就收摊（详见 sendEvent 的注释）。
func emitAssistantEvents(ctx context.Context, mv *adk.MessageVariant, out chan<- adkEvent) error {
	if !mv.IsStreaming {
		msg := mv.Message
		if msg.Content != "" && !sendEvent(ctx, out, adkEvent{kind: adkEventToken, text: msg.Content}) {
			return ctx.Err()
		}
		// 非流式分支的用量挂在消息自己身上（流式那边要靠 ConcatMessages 合并）。
		if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
			sendEvent(ctx, out, adkEvent{kind: adkEventUsage, usage: msg.ResponseMeta.Usage})
		}
		emitToolCalls(ctx, msg.ToolCalls, out)
		return nil
	}

	var chunks []*schema.Message
	for {
		chunk, err := mv.MessageStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			mv.MessageStream.Close()
			return fmt.Errorf("receive assistant stream: %w", err)
		}
		chunks = append(chunks, chunk)
		if chunk.Content != "" && !sendEvent(ctx, out, adkEvent{kind: adkEventToken, text: chunk.Content}) {
			mv.MessageStream.Close()
			return ctx.Err()
		}
	}
	mv.MessageStream.Close()

	full, err := schema.ConcatMessages(chunks)
	if err != nil {
		return fmt.Errorf("concat assistant stream chunks: %w", err)
	}
	// 【用量在这一步可得】ConcatMessages 会把各块的 ResponseMeta.Usage 合并
	// （取最大值，等价于取那个真正带 usage 的块）。这里把它送出去记为一条
	// 事件，由 consumeEvents 转给 llm.RecordUsage。
	if full.ResponseMeta != nil && full.ResponseMeta.Usage != nil {
		sendEvent(ctx, out, adkEvent{kind: adkEventUsage, usage: full.ResponseMeta.Usage})
	}
	emitToolCalls(ctx, full.ToolCalls, out)
	return nil
}

func emitToolCalls(ctx context.Context, calls []schema.ToolCall, out chan<- adkEvent) {
	for _, tc := range calls {
		if !sendEvent(ctx, out, adkEvent{
			kind:       adkEventToolCall,
			toolCallID: tc.ID,
			toolName:   tc.Function.Name,
			toolArgs:   json.RawMessage(tc.Function.Arguments),
		}) {
			return
		}
	}
}
