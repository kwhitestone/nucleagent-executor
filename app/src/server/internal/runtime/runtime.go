// Package runtime executor 运行时：把 core 下发的 a2a_request 派发到 Backend，
// 把执行事件回报给 core。
//
// 职责（参考 agentia-executor internal/executor + internal/executorbridge）：
//   - 按 req.Capability 路由到对应 Backend
//   - 管理 TaskSession 生命周期（创建/更新/完成）
//   - 把 StreamReporter 事件转成 a2a_stream_event 信封发回 core
//   - 收到 task_kill 时取消运行中 session
//   - executor-agnostic：不解释 req 的业务字段
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"github.com/google/uuid"

	"nucleagent-executor/addons/backend"
	"nucleagent-executor/addons/session"
)

// Runtime executor 运行时，实现 wsclient.Handler。
type Runtime struct {
	runner    *backend.Runner
	store     *session.Store
	sender    EnvelopeSender // 回报 core 的 WS 发送器

	maxSessions int

	mu       sync.Mutex
	running  map[uint]context.CancelFunc // conversationID -> cancel
}

// EnvelopeSender 发送 WS 信封的接口（由 wsclient.Client 实现，注入避免循环依赖）。
type EnvelopeSender interface {
	Send(kind string, payload any) error
	SendWithRequest(kind, requestID string, payload any) error
}

// New 构造运行时。sender 可为 nil（注册前占位），后续用 SetSender 注入。
func New(runner *backend.Runner, store *session.Store, sender EnvelopeSender, maxSessions int) *Runtime {
	if maxSessions <= 0 {
		maxSessions = 10
	}
	return &Runtime{
		runner:      runner,
		store:       store,
		sender:      sender,
		maxSessions: maxSessions,
		running:     make(map[uint]context.CancelFunc),
	}
}

// SetSender 注入 WS 发送器（注册成功、wsclient 建好后回填）。
func (r *Runtime) SetSender(sender EnvelopeSender) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sender = sender
}

// HandleEnvelope 处理 core 下发的信封。
func (r *Runtime) HandleEnvelope(ctx context.Context, env *a2a.Envelope) error {
	switch env.Type {
	case a2a.EnvA2ARequest:
		return r.handleRequest(ctx, env)
	case a2a.EnvTaskKill:
		return r.handleKill(ctx, env)
	case a2a.EnvHandshakeAck:
		// 握手确认，无需处理。
		return nil
	case a2a.EnvA2ATaskResultAck:
		// core 确认收到结果，无需处理（可用于幂等清理，后置）。
		return nil
	case a2a.EnvPong, a2a.EnvPing:
		return nil
	case a2a.EnvError:
		var p a2a.ErrorPayload
		_ = env.ParsePayload(&p)
		return fmt.Errorf("core error: %s: %s", p.Code, p.Message)
	default:
		return nil
	}
}

// handleRequest 处理 a2a_request：ACK 后异步执行。
func (r *Runtime) handleRequest(ctx context.Context, env *a2a.Envelope) error {
	var req a2a.A2ARequestPayload
	if err := env.ParsePayload(&req); err != nil {
		return fmt.Errorf("parse a2a_request: %w", err)
	}

	var execReq a2a.ExecutionRequest
	if err := json.Unmarshal(req.Body, &execReq); err != nil {
		return fmt.Errorf("unmarshal execution request: %w", err)
	}

	// 合并 payload.Headers 到 execReq.Headers（兜底：core 可能在 Headers 而非 Body 里带 x-llm-proxy-key）。
	// execReq.Headers 优先（Body 内的值不被 payload 覆盖）。
	if execReq.Headers == nil {
		execReq.Headers = req.Headers
	} else {
		for k, v := range req.Headers {
			if _, exists := execReq.Headers[k]; !exists {
				execReq.Headers[k] = v
			}
		}
	}

	// 容量检查。
	r.mu.Lock()
	if len(r.running) >= r.maxSessions {
		r.mu.Unlock()
		r.sendError(env.ID, "executor_capacity_full", fmt.Sprintf("max %d sessions reached", r.maxSessions), false)
		return nil
	}
	if _, exists := r.running[execReq.ConversationID]; exists {
		r.mu.Unlock()
		// 同 conversation 已在运行，拒绝重复执行。
		r.sendError(env.ID, "conversation_busy", "conversation already running", false)
		return nil
	}

	taskCtx, cancel := context.WithCancel(context.Background())
	r.running[execReq.ConversationID] = cancel
	r.mu.Unlock()

	// 先 ACK：a2a_response status=200（accepted）。
	_ = r.sender.SendWithRequest(a2a.EnvA2AResponse, env.ID, a2a.A2AResponsePayload{
		Status: 200,
	})

	// 异步执行，避免阻塞读循环。
	go r.execute(taskCtx, env, &execReq, req.Capability)
	return nil
}

// execute 执行任务并回报结果。
func (r *Runtime) execute(ctx context.Context, reqEnv *a2a.Envelope, req *a2a.ExecutionRequest, capability string) {
	defer func() {
		r.mu.Lock()
		delete(r.running, req.ConversationID)
		r.mu.Unlock()
	}()

	// 创建 session。
	sessID := uuid.NewString()
	sess := a2a.TaskSession{
		ID:             sessID,
		ConversationID: req.ConversationID,
		StepID:         req.StepID,
		Backend:        capability,
		Status:         "running",
		StartedAt:      nowMillis(),
	}
	_ = r.store.Create(sess)

	reporter := newCloudReporter(r.sender, req.ConversationID, req.StepID)

	// 执行（Kill 由 ctx cancel 触发）。
	result, runErr := r.runner.Run(ctx, capability, req, reporter)
	reporter.Flush()
	if runErr != nil && ctx.Err() != nil {
		// ctx 取消：归为 killed。
		result = a2a.ExecutionResult{StepID: req.StepID, Status: "killed", Error: "cancelled"}
	} else if runErr != nil {
		// backend 路由或执行错误：归为 failed。
		result = a2a.ExecutionResult{StepID: req.StepID, Status: "failed", Error: runErr.Error()}
	}

	// 更新 session 终态。
	endStatus := result.Status
	if endStatus == "" {
		endStatus = "failed"
	}
	if ctx.Err() != nil {
		endStatus = "killed"
		result.Status = "killed"
		result.Error = "cancelled"
	}
	_ = r.store.Update(sessID, endStatus)

	// 回报最终结果。
	resultBody, _ := json.Marshal(result)
	_ = r.sender.SendWithRequest(a2a.EnvA2ATaskResult, reqEnv.ID, a2a.A2ATaskResultPayload{
		ConversationID: req.ConversationID,
		StepID:         req.StepID,
		Status:         endStatus,
		Body:           resultBody,
	})
}

// handleKill 处理 task_kill：取消运行中 session。
func (r *Runtime) handleKill(ctx context.Context, env *a2a.Envelope) error {
	var p a2a.TaskKillPayload
	if err := env.ParsePayload(&p); err != nil {
		return fmt.Errorf("parse task_kill: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cid := range p.ConversationIDs {
		if cancel, ok := r.running[cid]; ok {
			cancel()
		}
	}
	return nil
}

// KillByConversation 供本地 admin/health 调用取消指定对话。
func (r *Runtime) KillByConversation(cid uint) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.running[cid]; ok {
		cancel()
		return true
	}
	return false
}

// RunningCount 当前运行中 session 数。
func (r *Runtime) RunningCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.running)
}

// sendError 发送错误信封给 core。
func (r *Runtime) sendError(requestID, code, message string, retry bool) {
	_ = r.sender.SendWithRequest(a2a.EnvError, requestID, a2a.ErrorPayload{
		Code:    code,
		Message: message,
		Retry:   retry,
	})
}

func nowMillis() int64 { return time.Now().UnixMilli() }
