package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Tool = (*Calculator)(nil)

// Calculator 是三个内置工具里唯一不碰任何外部依赖的——纯 Go 算术,
// 用来验证"Agent 主循环到工具调用协议"这条链路本身是通的（开发文档
// §7.1 的例子"3 个 128 的和乘以 2"就是靠它算出来的）,不掺杂检索/
// embedding 那些真实依赖是否配置好带来的变量。
type Calculator struct{}

func NewCalculator() *Calculator {
	return &Calculator{}
}

func (c *Calculator) Name() string { return "calculator" }
func (c *Calculator) Description() string {
	return "做基础算术运算（加减乘除），输入两个数字和一个运算符"
}

func (c *Calculator) Metadata() Metadata {
	return Metadata{SideEffectLevel: ReadOnly, RetryPolicy: RetrySafe}
}

const calculatorSchema = `{
	"type": "object",
	"properties": {
		"a": {"type": "number", "description": "第一个操作数"},
		"b": {"type": "number", "description": "第二个操作数"},
		"operator": {"type": "string", "enum": ["+", "-", "*", "/"], "description": "运算符"}
	},
	"required": ["a", "b", "operator"]
}`

func (c *Calculator) Spec() ToolSpec {
	return ToolSpec{Name: c.Name(), Description: c.Description(), Schema: json.RawMessage(calculatorSchema)}
}

type calculatorArgs struct {
	A        float64 `json:"a"`
	B        float64 `json:"b"`
	Operator string  `json:"operator"`
}

type calculatorResult struct {
	Result float64 `json:"result"`
}

func (c *Calculator) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a calculatorArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("parse calculator args: %w: %v", platform.ErrInvalid, err)
	}

	var result float64
	switch a.Operator {
	case "+":
		result = a.A + a.B
	case "-":
		result = a.A - a.B
	case "*":
		result = a.A * a.B
	case "/":
		if a.B == 0 {
			return nil, fmt.Errorf("division by zero: %w", platform.ErrInvalid)
		}
		result = a.A / a.B
	default:
		return nil, fmt.Errorf("unsupported operator %q: %w", a.Operator, platform.ErrInvalid)
	}

	return json.Marshal(calculatorResult{Result: result})
}
