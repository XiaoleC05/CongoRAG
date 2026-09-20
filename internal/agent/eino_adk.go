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

// adkEvent 是 Eino ADK 事件流的中性投影,usecase.go 消费的是这个类型,
// 从未导入任何 Eino 包。
type adkEvent struct {
	kind       string // token | tool_call | tool_result | error | done
	text       string
	toolCallID string
	toolName   string
	toolArgs   json.RawMessage
	toolResult json.RawMessage
	err        error
}

const (
	adkEventToken      = "token"
	adkEventToolCall   = "tool_call"
	adkEventToolResult = "tool_result"
	adkEventError      = "error"
	adkEventDone       = "done"
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

func (a *toolAdapter) InvokableRun(ctx context.Context, argsJSON string, opts ...tool.Option) (string, error) {
	result, err := a.t.Invoke(ctx, json.RawMessage(argsJSON))
	if err != nil {
		return "", err
	}
	return string(result), nil
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

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true})
	iter := runner.Query(ctx, input)

	out := make(chan adkEvent)
	go drainIterator(ctx, iter, out)
	return out, nil
}

func drainIterator(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], out chan<- adkEvent) {
	defer close(out)

	for {
		event, ok := iter.Next()
		if !ok {
			out <- adkEvent{kind: adkEventDone}
			return
		}
		if event.Err != nil {
			out <- adkEvent{kind: adkEventError, err: event.Err}
			return
		}

		mv := event.Output.MessageOutput
		if mv == nil {
			continue
		}

		switch mv.Role {
		case schema.Assistant:
			if err := emitAssistantEvents(mv, out); err != nil {
				out <- adkEvent{kind: adkEventError, err: err}
				return
			}
		case schema.Tool:
			msg, err := mv.GetMessage()
			if err != nil {
				out <- adkEvent{kind: adkEventError, err: fmt.Errorf("read tool result message: %w", err)}
				return
			}
			out <- adkEvent{
				kind: adkEventToolResult, toolCallID: msg.ToolCallID,
				toolName: mv.ToolName, toolResult: json.RawMessage(msg.Content),
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
func emitAssistantEvents(mv *adk.MessageVariant, out chan<- adkEvent) error {
	if !mv.IsStreaming {
		msg := mv.Message
		if msg.Content != "" {
			out <- adkEvent{kind: adkEventToken, text: msg.Content}
		}
		emitToolCalls(msg.ToolCalls, out)
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
		if chunk.Content != "" {
			out <- adkEvent{kind: adkEventToken, text: chunk.Content}
		}
	}
	mv.MessageStream.Close()

	full, err := schema.ConcatMessages(chunks)
	if err != nil {
		return fmt.Errorf("concat assistant stream chunks: %w", err)
	}
	emitToolCalls(full.ToolCalls, out)
	return nil
}

func emitToolCalls(calls []schema.ToolCall, out chan<- adkEvent) {
	for _, tc := range calls {
		out <- adkEvent{
			kind:       adkEventToolCall,
			toolCallID: tc.ID,
			toolName:   tc.Function.Name,
			toolArgs:   json.RawMessage(tc.Function.Arguments),
		}
	}
}
