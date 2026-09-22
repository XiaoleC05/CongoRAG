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

func (a *toolAdapter) InvokableRun(ctx context.Context, argsJSON string, opts ...tool.Option) (string, error) {
	result, err := a.t.Invoke(ctx, json.RawMessage(argsJSON))
	if err == nil {
		return string(result), nil
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
				schema.ToolMessage(string(turn.ToolResult), callID),
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
