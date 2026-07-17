package backend

import (
	"fmt"
	"sync"
)

// Registry 后端注册表，按 Capability 查找。
type Registry struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{backends: make(map[string]Backend)}
}

// Default 默认全局注册表，供各后端在 init() 中自注册。
var Default = NewRegistry()

// Register 注册后端（同 Capability 覆盖）。
func (r *Registry) Register(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[b.Capability()] = b
}

// Get 按 capability 查找后端，未注册返回错误。
func (r *Registry) Get(capability string) (Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[capability]
	if !ok {
		return nil, fmt.Errorf("backend: capability %q not registered", capability)
	}
	return b, nil
}

// Names 返回所有已注册 capability。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	return names
}
