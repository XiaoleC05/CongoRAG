package agent

import (
	"fmt"
	"sync"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Registry = (*toolRegistry)(nil)

// toolRegistry 是 Registry 唯一的实现——一个加锁的 map,技术方案 §4.1
// 的注册表模式：装配根在启动时 Register 三个内置工具,Usecase.Start
// 按 Agent.ToolNames 过滤出这次执行允许用哪些,工具本身的增减不影响
// 这个类型或 Usecase 主循环的代码。
type toolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *toolRegistry {
	return &toolRegistry{tools: make(map[string]Tool)}
}

func (r *toolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

func (r *toolRegistry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool %q: %w", name, platform.ErrNotFound)
	}
	return t, nil
}

func (r *toolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
