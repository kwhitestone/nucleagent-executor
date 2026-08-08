package hermes

import (
	"encoding/json"
	"testing"
)

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
