// Package backend 可插拔执行后端：定义 Backend 接口、注册表与 runner。
// 仅含接口与基础设施，具体后端（OpenCode/Hermes）在子包中实现并自注册。
package backend

import (
	"context"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// Backend 可插拔执行后端接口（OpenCode / Hermes 等实现）。
type Backend interface {
	// Capability 返回后端能力标识，用于注册表查找（如 "opencode"）。
	Capability() string
	// Run 执行任务，通过 reporter 回报流式事件，返回同步结果摘要。
	Run(ctx context.Context, req *a2a.ExecutionRequest, reporter a2a.StreamReporter) a2a.ExecutionResult
	// Kill 终止指定会话。
	Kill(ctx context.Context, session a2a.TaskSession) error
}
