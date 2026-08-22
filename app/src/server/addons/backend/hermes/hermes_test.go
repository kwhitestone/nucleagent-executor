package hermes

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fakeEventSource 内存实现 eventSource，按顺序投递预设事件。
type fakeEventSource struct {
	events chan GatewayEvent
	done   chan struct{}
}

func newFakeEventSource(evts ...GatewayEvent) *fakeEventSource {
	ch := make(chan GatewayEvent, len(evts))
	for _, e := range evts {
		ch <- e
	}
	// 投完不 close events —— close done 模拟连接断开（仅在测试想触发那条路径时）。
	return &fakeEventSource{events: ch, done: make(chan struct{})}
}

func (f *fakeEventSource) Events() <-chan GatewayEvent { return f.events }
func (f *fakeEventSource) Done() <-chan struct{}       { return f.done }

// noopReporter 满足 a2a.StreamReporter，忽略所有回调。
type noopReporter struct{}

func (noopReporter) TextDelta(string)       {}
func (noopReporter) ThinkingDelta(string)   {}
func (noopReporter) Progress(string)        {}
func (noopReporter) ToolUse(string, string) {}
func (noopReporter) Flush()                 {}

// TestDrainEventsReturnsOnComplete 收到 message.complete 必须立即返回，
// 不能再等任何时间窗口。这是本测试的核心目的：守住「complete 即返回」这条边界，
// 防止有人重新加回 complete 后的等待逻辑（之前的 30s 空等曾把「答完即追问」误判成 409）。
//
// 判据不是「最终返回了」而是「用了多久」—— 超过 2s 即判定又加回了等待逻辑。
func TestDrainEventsReturnsOnComplete(t *testing.T) {
	src := newFakeEventSource(
		GatewayEvent{EventType: "message.delta", Payload: json.RawMessage(`{"text":"hi"}`)},
		GatewayEvent{EventType: "message.complete", Payload: json.RawMessage(`{"text":"hi"}`)},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	out, status, errMsg, _ := drainEvents(ctx, src, "s1", noopReporter{})
	elapsed := time.Since(start)

	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	if errMsg != "" {
		t.Errorf("errMsg = %q, want empty", errMsg)
	}
	if out != "hi" {
		t.Errorf("output = %q, want hi", out)
	}
	// complete 一到就该返回。给 2s 余量（通道调度），超过说明又加了等待窗口。
	if elapsed > 2*time.Second {
		t.Errorf("drainEvents 用了 %v 才返回，应在收到 complete 后立即返回（≤2s）", elapsed)
	}
}

// TestDrainEventsIdleTimeoutStillWorks 无事件时不永久阻塞。
// 测试用短 ctx 超时注入 —— 不真等 5min，只验证「无事件会超时退出」这条路径，
// 说明 idle 保护机制（ctx 兜底 / 5min timer）仍在生效。
func TestDrainEventsIdleTimeoutStillWorks(t *testing.T) {
	src := newFakeEventSource() // 空：不投任何事件
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, status, errMsg, _ := drainEvents(ctx, src, "s1", noopReporter{})

	// ctx 先于 5min idle 触发 → 走 ctx.Done() 分支，status=killed。
	if status != "killed" {
		t.Errorf("status = %q, want killed (ctx cancelled before any event)", status)
	}
	if errMsg != "cancelled" {
		t.Errorf("errMsg = %q, want cancelled", errMsg)
	}
}

// TestDrainEventsConnectionClosedBeforeComplete 连接断开且没收到 complete → 失败。
// 保护「hermes 进程崩了 / 网络断了」的场景，不能静默挂起。
func TestDrainEventsConnectionClosedBeforeComplete(t *testing.T) {
	src := newFakeEventSource(
		GatewayEvent{EventType: "message.delta", Payload: json.RawMessage(`{"text":"partial"}`)},
	)
	// 模拟连接断开（Done 关闭）。事件通道还有缓冲的 delta，但不会有 complete。
	close(src.done)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, status, _, _ := drainEvents(ctx, src, "s1", noopReporter{})

	if status != "failed" {
		t.Errorf("status = %q, want failed (connection closed before complete)", status)
	}
}

// TestParseReadyPort 验证 hermes serve 端口 sentinel 解析。
// 对齐 agentia-executor-hermes/shell/src/hermes/process.rs 的测试用例。
func TestParseReadyPort(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"HERMES_BACKEND_READY port=9119", 9119},
		{"HERMES_DASHBOARD_READY port=41234", 41234},
		{"  HERMES_BACKEND_READY port=8080  ", 8080},
		{"HERMES_BACKEND_READY", 0},                        // 无 port=
		{"HERMES_BACKEND_READY port=abc", 0},               // 非数字
		{"Hermes backend listening on 127.0.0.1:9119", 0},  // 无前缀
		{"", 0},
		{"some unrelated log line", 0},
	}
	for _, c := range cases {
		got := parseReadyPort(c.line)
		if got != c.want {
			t.Errorf("parseReadyPort(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

// TestParseEvent 验证 gateway 事件帧解析（method=="event"）。
func TestParseEvent(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"type":       "message.delta",
		"session_id": "s1",
		"payload":    map[string]any{"text": "hi"},
	})
	evt := parseEvent(params)
	if evt.EventType != "message.delta" {
		t.Errorf("EventType = %q, want message.delta", evt.EventType)
	}
	if evt.SessionID != "s1" {
		t.Errorf("SessionID = %q, want s1", evt.SessionID)
	}
	if got := extractText(evt.Payload); got != "hi" {
		t.Errorf("text = %q, want hi", got)
	}
}

// TestParseEventEmpty 空 params 不 panic、返回零值。
func TestParseEventEmpty(t *testing.T) {
	evt := parseEvent(nil)
	if evt.EventType != "" {
		t.Errorf("empty params EventType = %q, want empty", evt.EventType)
	}
}

// TestYamlScalar 验证 YAML 标量转义（对齐 managed.rs 的 yaml_scalar）。
func TestYamlScalar(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gpt-4o", "gpt-4o"},                     // 普通 id，裸用
		{"", `""`},                               // 空串引号
		{"a:b", `"a:b"`},                         // 含冒号引号
		{`key"with"quote`, `"key\"with\"quote"`}, // 双引号转义
		{"has space", "has space"},               // 空格不需引号
		{"[bracket]", `"[bracket]"`},             // 方括号引号
	}
	for _, c := range cases {
		got := yamlScalar(c.in)
		if got != c.want {
			t.Errorf("yamlScalar(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExtractToolName tool 事件 payload 取名（兼容 name/tool 两字段）。
func TestExtractToolName(t *testing.T) {
	p1, _ := json.Marshal(map[string]any{"name": "write_file"})
	if got := extractToolName(p1); got != "write_file" {
		t.Errorf("got %q, want write_file", got)
	}
	p2, _ := json.Marshal(map[string]any{"tool": "read_file"})
	if got := extractToolName(p2); got != "read_file" {
		t.Errorf("got %q, want read_file", got)
	}
}
