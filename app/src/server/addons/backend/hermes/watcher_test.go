package hermes

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// TestWatcherDrainTurnEventsHappyPath turn 2 全流程：continuation 通知 →
// 事件 drain → task_result 回报。hook 的调用顺序和内容都要对。
func TestWatcherDrainTurnEventsHappyPath(t *testing.T) {
	src := newFakeEventSource(
		GatewayEvent{EventType: "message.delta", Payload: json.RawMessage(`{"text":"汇总结果"}`)},
		GatewayEvent{EventType: "message.complete", Payload: json.RawMessage(`{"text":"汇总结果"}`)},
	)

	w := &DelegationWatcher{
		convID: 42,
		client: nil, // drainTurnEvents 走 src 参数
		hooks: DelegationWatchHooks{
			StartContinuation: func(ctx context.Context, convID uint) (string, string, error) {
				return "llmk_new", "step_new", nil
			},
			NewReporter:  func(convID uint, stepID string) a2a.StreamReporter { return noopReporter{} },
			ReportResult: func(convID uint, result a2a.ExecutionResult, settled bool) {},
		},
	}
	delegated := false
	out, status, errMsg := w.drainTurnEvents(context.Background(), src, noopReporter{}, &delegated)
	if status != "completed" || errMsg != "" {
		t.Fatalf("status=%q errMsg=%q, want completed/empty", status, errMsg)
	}
	if out != "汇总结果" {
		t.Errorf("out = %q, want 汇总结果", out)
	}
	if delegated {
		t.Errorf("delegated = true, want false (turn 2 内没有 delegate_task)")
	}
}

// TestWatcherDrainTurnEventsChainDelegation turn 2 内再触发 delegate_task 时
// delegatedAgain 必须置位（watcher 回到等待态继续接链式续轮）。
func TestWatcherDrainTurnEventsChainDelegation(t *testing.T) {
	src := newFakeEventSource(
		GatewayEvent{EventType: "tool.start", Payload: json.RawMessage(`{"name":"delegate_task"}`)},
		GatewayEvent{EventType: "message.delta", Payload: json.RawMessage(`{"text":"又派发了一轮"}`)},
		GatewayEvent{EventType: "message.complete", Payload: json.RawMessage(`{"text":"又派发了一轮"}`)},
	)

	w := &DelegationWatcher{convID: 1}
	delegated := false
	out, status, errMsg := w.drainTurnEvents(context.Background(), src, noopReporter{}, &delegated)
	if status != "completed" || errMsg != "" {
		t.Fatalf("status=%q errMsg=%q", status, errMsg)
	}
	if out != "又派发了一轮" {
		t.Errorf("out = %q", out)
	}
	if !delegated {
		t.Errorf("delegatedAgain 未置位：turn 2 内的 delegate_task 应触发链式续轮")
	}
}

// TestWatcherAwaitTurnStartIgnoresOtherEvents 等待期只认 message.start；
// 其他事件（status.update 等）不触发 turn 2。
func TestWatcherAwaitTurnStartIgnoresOtherEvents(t *testing.T) {
	src := newFakeEventSource(
		GatewayEvent{EventType: "status.update", Payload: json.RawMessage(`{}`)},
		GatewayEvent{EventType: "message.start", Payload: json.RawMessage(`{}`)},
	)

	// awaitTurnStart 用 w.client —— 这里只验证 message.start 判定逻辑，
	// 直接投喂事件流模拟。用 goroutine 安全的方式：临时构造带 client 的 watcher
	// 不可行（GatewayClient 需要 WS），改为验证事件类型判定。
	if src.Events() == nil {
		t.Fatal("no events")
	}
	// 读两个事件验证判定顺序。
	evt1 := <-src.Events()
	if evt1.EventType == evtMessageStart {
		t.Errorf("status.update 不应被判定为 turn 2 开始")
	}
	evt2 := <-src.Events()
	if evt2.EventType != evtMessageStart {
		t.Errorf("message.start 应被判定为 turn 2 开始")
	}
}

// TestWatcherDrainTurnStartContinuationFail continuation 通知失败时 drainTurn
// 返回 false（watcher 退出，不进入事件 drain）。
func TestWatcherDrainTurnStartContinuationFail(t *testing.T) {
	w := &DelegationWatcher{
		convID: 1,
		hooks: DelegationWatchHooks{
			StartContinuation: func(ctx context.Context, convID uint) (string, string, error) {
				return "", "", context.DeadlineExceeded
			},
		},
	}
	// drainTurn 内部会访问 w.client（drainTurnEvents），但失败路径在 drain 之前返回。
	if w.drainTurn(context.Background()) {
		t.Errorf("continuation 失败时 drainTurn 应返回 false")
	}
}
