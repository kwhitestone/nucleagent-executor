// Package opencode OpenCode 执行后端骨架。
// 真实执行（经 core LLM Proxy 临时 Key 调用 LLM、子进程管理）待实现。
package opencode

import (
	"context"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"nucleagent-executor/addons/backend"
)

// Capability OpenCode 后端的能力标识。
const Capability = "opencode"

// OpenCodeBackend OpenCode 执行后端骨架。
type OpenCodeBackend struct{}

func init() {
	backend.Default.Register(&OpenCodeBackend{})
}

// Capability 返回能力标识。
func (b *OpenCodeBackend) Capability() string { return Capability }

// Run 骨架实现：回报占位文本后返回 completed（真实 LLM 执行待接入）。
func (b *OpenCodeBackend) Run(ctx context.Context, req *a2a.ExecutionRequest, reporter a2a.StreamReporter) a2a.ExecutionResult {
	reporter.TextDelta("[opencode skeleton] execution not implemented")
	reporter.Flush()
	return a2a.ExecutionResult{
		StepID: req.StepID,
		Status: "completed",
	}
}

// Kill 终止会话（TODO：终止 OpenCode 子进程）。
func (b *OpenCodeBackend) Kill(ctx context.Context, session a2a.TaskSession) error {
	return nil
}
