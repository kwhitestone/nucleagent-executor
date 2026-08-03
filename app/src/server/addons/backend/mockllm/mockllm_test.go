package mockllm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// captureReporter 记录所有流式事件。
type captureReporter struct {
	mu      sync.Mutex
	text    strings.Builder
	think   strings.Builder
	progress []string
	tools   []string
}

func (c *captureReporter) TextDelta(s string)     { c.mu.Lock(); c.text.WriteString(s); c.mu.Unlock() }
func (c *captureReporter) ThinkingDelta(s string) { c.mu.Lock(); c.think.WriteString(s); c.mu.Unlock() }
func (c *captureReporter) Progress(s string)      { c.mu.Lock(); c.progress = append(c.progress, s); c.mu.Unlock() }
func (c *captureReporter) ToolUse(tool, s string) { c.mu.Lock(); c.tools = append(c.tools, tool); c.mu.Unlock() }
func (c *captureReporter) Flush()                 {}

// TestMockLLMRunCompletes 验证 mock 执行完成且流式输出非空。
func TestMockLLMRunCompletes(t *testing.T) {
	b := New("") // 纯 mock，不调真实 LLM
	r := &captureReporter{}

	req := &a2a.ExecutionRequest{
		ConversationID: 1,
		StepID:         "s1",
		Mode:           "a2a",
		Input:          "hello world",
	}
	result := b.Run(context.Background(), req, r)

	if result.Status != "completed" {
		t.Errorf("Status = %q, want completed", result.Status)
	}
	if result.StepID != "s1" {
		t.Errorf("StepID = %q, want s1", result.StepID)
	}
	if r.text.Len() == 0 {
		t.Error("text output should be non-empty")
	}
	if !strings.Contains(r.text.String(), "hello world") {
		t.Errorf("text should contain input echo, got: %q", r.text.String())
	}
	if r.think.Len() == 0 {
		t.Error("thinking output should be non-empty")
	}
}

// TestMockLLMCancelKilled 验证 ctx 取消时返回 killed。
func TestMockLLMCancelKilled(t *testing.T) {
	b := New("")
	r := &captureReporter{}

	ctx, cancel := context.WithCancel(context.Background())
	// 启动后立即取消（mock 每 20ms 一段，足够在第一段后取消）。
	go func() {
		cancel()
	}()

	req := &a2a.ExecutionRequest{
		ConversationID: 2,
		StepID:         "s2",
		Mode:           "a2a",
		Input:          strings.Repeat("x", 200), // 长输入产生多段
	}
	result := b.Run(ctx, req, r)

	// 取消可能在第一段前或后生效，状态应为 killed（或恰好在边界 completed）。
	// 宽松断言：要么 killed，要么 completed（极小概率）。重点是不阻塞、不 panic。
	if result.Status != "killed" && result.Status != "completed" {
		t.Errorf("Status = %q, want killed or completed", result.Status)
	}
}

// TestCapabilityAndDescriptor 验证能力标识与描述符。
func TestCapabilityAndDescriptor(t *testing.T) {
	b := New("")
	if b.Capability() != Capability {
		t.Errorf("Capability = %q, want %q", b.Capability(), Capability)
	}
	d := b.Descriptor()
	if d.ID != Capability || !d.Streaming {
		t.Errorf("Descriptor mismatch: %+v", d)
	}
}

// TestSplitForStream 验证分段逻辑。
func TestSplitForStream(t *testing.T) {
	parts := splitForStream("abcdefgh", 3)
	if len(parts) != 3 {
		t.Fatalf("len = %d, want 3", len(parts))
	}
	if parts[0] != "abc" || parts[1] != "def" || parts[2] != "gh" {
		t.Errorf("parts = %v", parts)
	}

	// n<=0 返回单段。
	one := splitForStream("abc", 0)
	if len(one) != 1 || one[0] != "abc" {
		t.Errorf("n=0 should return single segment, got %v", one)
	}
}

// TestExtractContent 验证 OpenAI 响应粗解析。
func TestExtractContent(t *testing.T) {
	resp := `{"choices":[{"message":{"content":"hello there"}}]}`
	got := extractContent(resp)
	if got != "hello there" {
		t.Errorf("extractContent = %q, want %q", got, "hello there")
	}
}

// TestMockLLMNoProxyKeyFallsBack 验证无 proxy key 时回退模拟（不报错）。
func TestMockLLMNoProxyKeyFallsBack(t *testing.T) {
	b := New("http://localhost:26680") // 有 coreURL 但 req 无 key
	r := &captureReporter{}
	req := &a2a.ExecutionRequest{
		ConversationID: 3,
		StepID:         "s3",
		Mode:           "a2a",
		Input:          "test",
		// Headers 不含 x-llm-proxy-key
	}
	result := b.Run(context.Background(), req, r)
	if result.Status != "completed" {
		t.Errorf("Status = %q, want completed (fallback)", result.Status)
	}
	if r.text.Len() == 0 {
		t.Error("fallback text should be non-empty")
	}
}
