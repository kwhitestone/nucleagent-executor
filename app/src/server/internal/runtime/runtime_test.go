package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"nucleagent-executor/addons/backend"
	"nucleagent-executor/addons/session"
)

// fakeSender 记录所有发送的信封。
type fakeSender struct {
	mu     sync.Mutex
	envs   []*a2a.Envelope
	byType map[string][]*a2a.Envelope
}

func newFakeSender() *fakeSender {
	return &fakeSender{byType: map[string][]*a2a.Envelope{}}
}

func (f *fakeSender) Send(kind string, payload any) error {
	return f.send(kind, "", payload)
}

func (f *fakeSender) SendWithRequest(kind, requestID string, payload any) error {
	return f.send(kind, requestID, payload)
}

func (f *fakeSender) send(kind, requestID string, payload any) error {
	env, _ := a2a.NewEnvelope(time.Now().UnixMilli(), kind, payload)
	env.RequestID = requestID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envs = append(f.envs, env)
	f.byType[kind] = append(f.byType[kind], env)
	return nil
}

func (f *fakeSender) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byType[kind])
}

func (f *fakeSender) last(kind string) *a2a.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	lst := f.byType[kind]
	if len(lst) == 0 {
		return nil
	}
	return lst[len(lst)-1]
}

// fakeBackend 可控的测试后端：慢执行，可被 ctx 取消。
type fakeBackend struct {
	capability string
	started    chan struct{}
	delay      time.Duration
}

func (b *fakeBackend) Capability() string { return b.capability }
func (b *fakeBackend) Run(ctx context.Context, req *a2a.ExecutionRequest, reporter a2a.StreamReporter) a2a.ExecutionResult {
	if b.started != nil {
		close(b.started)
	}
	reporter.TextDelta("starting")
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return a2a.ExecutionResult{StepID: req.StepID, Status: "killed", Error: "cancelled"}
		case <-time.After(b.delay):
		}
		reporter.TextDelta(".")
	}
	reporter.Flush()
	return a2a.ExecutionResult{StepID: req.StepID, Status: "completed", Output: "done"}
}
func (b *fakeBackend) Kill(ctx context.Context, session a2a.TaskSession) error { return nil }

// setupTestRuntime 构造 runtime + 注入 fake backend。
func setupTestRuntime(t *testing.T, b backend.Backend) (*Runtime, *fakeSender) {
	t.Helper()
	reg := backend.NewRegistry()
	reg.Register(b)
	runner := backend.NewRunner(reg)
	store := session.NewStore("")
	sender := newFakeSender()
	rt := New(runner, store, sender, 10)
	return rt, sender
}

// buildA2ARequest 构造一个 a2a_request 信封。
func buildA2ARequest(t *testing.T, reqID string, req *a2a.ExecutionRequest, capability string) *a2a.Envelope {
	t.Helper()
	body, _ := json.Marshal(req)
	payload := a2a.A2ARequestPayload{
		Method:     "message/send",
		Capability: capability,
		Body:       body,
		Stream:     true,
	}
	env, _ := a2a.NewEnvelopeWithRequest(time.Now().UnixMilli(), a2a.EnvA2ARequest, reqID, payload)
	return env
}

// TestRuntimeRequestDispatchAndResult 验证 a2a_request -> 执行 -> a2a_task_result。
func TestRuntimeRequestDispatchAndResult(t *testing.T) {
	fb := &fakeBackend{capability: "fake", delay: 2 * time.Millisecond}
	rt, sender := setupTestRuntime(t, fb)

	req := &a2a.ExecutionRequest{
		ConversationID: 100,
		StepID:         "step-100",
		Mode:           "a2a",
		Input:          "hi",
	}
	env := buildA2ARequest(t, "req-1", req, "fake")

	if err := rt.HandleEnvelope(context.Background(), env); err != nil {
		t.Fatalf("HandleEnvelope: %v", err)
	}

	// 立即应收到 a2a_response ACK（status 200）。
	if got := sender.count(a2a.EnvA2AResponse); got < 1 {
		t.Errorf("expected >=1 a2a_response, got %d", got)
	}

	// 等待执行完成（fake backend 跑 10 * 2ms ≈ 20ms）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sender.count(a2a.EnvA2ATaskResult) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := sender.count(a2a.EnvA2ATaskResult); got != 1 {
		t.Fatalf("expected 1 a2a_task_result, got %d", got)
	}
	result := sender.last(a2a.EnvA2ATaskResult)
	var resultPayload a2a.A2ATaskResultPayload
	if err := result.ParsePayload(&resultPayload); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if resultPayload.Status != "completed" {
		t.Errorf("result status = %q, want completed", resultPayload.Status)
	}
	if resultPayload.ConversationID != 100 {
		t.Errorf("result conversationID = %d, want 100", resultPayload.ConversationID)
	}

	// 应有 text_delta 流式事件。
	if got := sender.count(a2a.EnvA2AStreamEvent); got == 0 {
		t.Error("expected stream events")
	}

	// session 应已落终态。
	// (store 按 session ID 存，runtime 用 uuid；这里只验证 running count 归零)
	if rt.RunningCount() != 0 {
		t.Errorf("after completion, RunningCount = %d, want 0", rt.RunningCount())
	}
}

// TestRuntimeKill 验证 task_kill 取消运行中任务。
func TestRuntimeKill(t *testing.T) {
	fb := &fakeBackend{capability: "fake", delay: 50 * time.Millisecond, started: make(chan struct{})}
	rt, sender := setupTestRuntime(t, fb)

	req := &a2a.ExecutionRequest{
		ConversationID: 200,
		StepID:         "step-200",
		Mode:           "a2a",
		Input:          "long running",
	}
	env := buildA2ARequest(t, "req-2", req, "fake")
	_ = rt.HandleEnvelope(context.Background(), env)

	// 等 backend 真正开始。
	select {
	case <-fb.started:
	case <-time.After(time.Second):
		t.Fatal("backend did not start")
	}

	// 发 task_kill。
	killEnv, _ := a2a.NewEnvelope(time.Now().UnixMilli(), a2a.EnvTaskKill, a2a.TaskKillPayload{
		ConversationIDs: []uint{200},
	})
	if err := rt.HandleEnvelope(context.Background(), killEnv); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// 等待 a2a_task_result，状态应为 killed。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sender.count(a2a.EnvA2ATaskResult) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	result := sender.last(a2a.EnvA2ATaskResult)
	if result == nil {
		t.Fatal("no task result after kill")
	}
	var p a2a.A2ATaskResultPayload
	_ = result.ParsePayload(&p)
	if p.Status != "killed" {
		t.Errorf("after kill, status = %q, want killed", p.Status)
	}
	if rt.RunningCount() != 0 {
		t.Errorf("after kill, RunningCount = %d, want 0", rt.RunningCount())
	}
}

// TestRuntimeCapacityFull 验证超过 maxSessions 时拒绝执行。
func TestRuntimeCapacityFull(t *testing.T) {
	fb := &fakeBackend{capability: "fake", delay: 100 * time.Millisecond, started: make(chan struct{})}
	reg := backend.NewRegistry()
	reg.Register(fb)
	runner := backend.NewRunner(reg)
	store := session.NewStore("")
	sender := newFakeSender()
	rt := New(runner, store, sender, 1) // max 1

	// 启动 1 个。
	req1 := &a2a.ExecutionRequest{ConversationID: 1, StepID: "s1", Mode: "a2a", Input: "a"}
	_ = rt.HandleEnvelope(context.Background(), buildA2ARequest(t, "r1", req1, "fake"))
	<-fb.started

	// 第 2 个应被拒（容量满），收到 error 信封。
	req2 := &a2a.ExecutionRequest{ConversationID: 2, StepID: "s2", Mode: "a2a", Input: "b"}
	_ = rt.HandleEnvelope(context.Background(), buildA2ARequest(t, "r2", req2, "fake"))
	if got := sender.count(a2a.EnvError); got < 1 {
		t.Errorf("expected error envelope for capacity full, got %d", got)
	}

	// 清理：kill 第 1 个。
	killEnv, _ := a2a.NewEnvelope(time.Now().UnixMilli(), a2a.EnvTaskKill, a2a.TaskKillPayload{ConversationIDs: []uint{1}})
	_ = rt.HandleEnvelope(context.Background(), killEnv)
}

// TestRuntimeDuplicateConversation 验证同 conversation 重复执行被拒。
func TestRuntimeDuplicateConversation(t *testing.T) {
	fb := &fakeBackend{capability: "fake", delay: 100 * time.Millisecond, started: make(chan struct{})}
	rt, sender := setupTestRuntime(t, fb)

	req := &a2a.ExecutionRequest{ConversationID: 300, StepID: "s", Mode: "a2a", Input: "x"}
	_ = rt.HandleEnvelope(context.Background(), buildA2ARequest(t, "r1", req, "fake"))
	<-fb.started

	// 同 conversationID 再发一次，应被拒。
	_ = rt.HandleEnvelope(context.Background(), buildA2ARequest(t, "r2", req, "fake"))
	if got := sender.count(a2a.EnvError); got < 1 {
		t.Errorf("expected error for duplicate conversation, got %d", got)
	}

	// 清理。
	killEnv, _ := a2a.NewEnvelope(time.Now().UnixMilli(), a2a.EnvTaskKill, a2a.TaskKillPayload{ConversationIDs: []uint{300}})
	_ = rt.HandleEnvelope(context.Background(), killEnv)
}

// TestRuntimeUnknownBackend 验证未知 capability 路由失败归为 failed。
func TestRuntimeUnknownBackend(t *testing.T) {
	fb := &fakeBackend{capability: "fake", delay: time.Millisecond}
	rt, sender := setupTestRuntime(t, fb)

	req := &a2a.ExecutionRequest{ConversationID: 400, StepID: "s", Mode: "a2a", Input: "x"}
	// 请求里 capability=nonexistent。
	_ = rt.HandleEnvelope(context.Background(), buildA2ARequest(t, "r1", req, "nonexistent"))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if sender.count(a2a.EnvA2ATaskResult) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	result := sender.last(a2a.EnvA2ATaskResult)
	if result == nil {
		t.Fatal("no result for unknown backend")
	}
	var p a2a.A2ATaskResultPayload
	_ = result.ParsePayload(&p)
	if p.Status != "failed" {
		t.Errorf("unknown backend status = %q, want failed", p.Status)
	}
}
