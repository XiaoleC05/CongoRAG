package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculator_Invoke_AllOperators(t *testing.T) {
	c := NewCalculator()
	cases := []struct {
		operator string
		a, b     float64
		want     float64
	}{
		{"+", 3, 4, 7},
		{"-", 10, 4, 6},
		{"*", 128, 3, 384}, // 开发文档 §7.1 的例子会用到这一步
		{"/", 10, 4, 2.5},
	}

	for _, tc := range cases {
		args, err := json.Marshal(map[string]any{"a": tc.a, "b": tc.b, "operator": tc.operator})
		require.NoError(t, err)

		raw, err := c.Invoke(context.Background(), args)
		require.NoError(t, err)

		var result calculatorResult
		require.NoError(t, json.Unmarshal(raw, &result))
		assert.Equal(t, tc.want, result.Result, "%v %s %v", tc.a, tc.operator, tc.b)
	}
}

func TestCalculator_Invoke_DivisionByZero_Errors(t *testing.T) {
	c := NewCalculator()
	args, _ := json.Marshal(map[string]any{"a": 1, "b": 0, "operator": "/"})

	_, err := c.Invoke(context.Background(), args)
	require.Error(t, err)
}

func TestCalculator_Invoke_UnsupportedOperator_Errors(t *testing.T) {
	c := NewCalculator()
	args, _ := json.Marshal(map[string]any{"a": 1, "b": 2, "operator": "^"})

	_, err := c.Invoke(context.Background(), args)
	require.Error(t, err)
}

func TestCalculator_Invoke_MalformedArgs_Errors(t *testing.T) {
	c := NewCalculator()
	_, err := c.Invoke(context.Background(), []byte(`not json`))
	require.Error(t, err)
}

func TestCalculator_Spec_SchemaIsValidJSON(t *testing.T) {
	c := NewCalculator()
	spec := c.Spec()

	var schema map[string]any
	require.NoError(t, json.Unmarshal(spec.Schema, &schema))
	assert.Equal(t, "calculator", spec.Name)
}

func TestCalculator_Metadata_IsReadOnly(t *testing.T) {
	c := NewCalculator()
	assert.Equal(t, ReadOnly, c.Metadata().SideEffectLevel)
}
