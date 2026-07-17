package backend

import (
	"context"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// Runner 通过注册表解析后端并驱动执行。
type Runner struct {
	registry *Registry
}

// NewRunner 基于注册表创建 runner。
func NewRunner(r *Registry) *Runner {
	return &Runner{registry: r}
}

// Run 按 capability 解析后端并执行；reporter 为空时使用空实现，避免 nil 调用。
func (r *Runner) Run(ctx context.Context, capability string, req *a2a.ExecutionRequest, reporter a2a.StreamReporter) (a2a.ExecutionResult, error) {
	b, err := r.registry.Get(capability)
	if err != nil {
		return a2a.ExecutionResult{StepID: req.StepID, Status: "failed", Error: err.Error()}, err
	}
	if reporter == nil {
		reporter = noopReporter{}
	}
	return b.Run(ctx, req, reporter), nil
}

// noopReporter StreamReporter 空实现。
type noopReporter struct{}

func (noopReporter) TextDelta(string)        {}
func (noopReporter) ThinkingDelta(string)    {}
func (noopReporter) Progress(string)         {}
func (noopReporter) ToolUse(string, string)  {}
func (noopReporter) Flush()                  {}
