// Package mockllm 一个用于打通协议的 Mock 执行后端。
//
// 真实 OpenCode 桥接后置；此前用 MockLLMBackend 验证 core↔executor 的
// WS 协议、StreamReporter、SessionStore、a2a_task_result 全链路。
//
// 它会：
//   - 回报一段 thinking_delta（模拟中间思考）
//   - 回报若干 text_delta（模拟流式最终回答，逐段 sleep）
//   - 可选经 core LLM Proxy 调真实 LLM（headers.x-llm-proxy-key 存在时）
//   - 返回 completed 结果
package mockllm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"github.com/nucleagent/nucleagent-shared/llm"

	"nucleagent-executor/addons/backend"
)

// Capability MockLLM 后端能力标识。
const Capability = "mock-llm"

// MockLLMBackend Mock 执行后端。
type MockLLMBackend struct {
	coreURL string // core API 地址（用于 LLM Proxy），空则纯模拟
}

func init() {
	backend.Default.Register(&MockLLMBackend{})
}

// New 构造 MockLLMBackend。coreURL 非空时尝试经 core LLM Proxy 调真实 LLM。
func New(coreURL string) *MockLLMBackend {
	return &MockLLMBackend{coreURL: coreURL}
}

// Capability 返回能力标识。
func (b *MockLLMBackend) Capability() string { return Capability }

// Descriptor 返回后端自描述（握手时上报）。
func (b *MockLLMBackend) Descriptor() a2a.DesktopExecutor {
	return a2a.DesktopExecutor{
		ID:          Capability,
		Type:        "mock",
		DisplayName: "Mock LLM",
		Streaming:   true,
	}
}

// Run 执行任务：模拟流式输出，可选调真实 LLM。
func (b *MockLLMBackend) Run(ctx context.Context, req *a2a.ExecutionRequest, reporter a2a.StreamReporter) a2a.ExecutionResult {
	// 1. 回报思考过程。
	reporter.ThinkingDelta(fmt.Sprintf("收到任务（mode=%s），正在思考…", req.Mode))

	// 2. 尝试调真实 LLM；失败则回退模拟文本。
	output, err := b.callLLM(ctx, req)
	if err != nil {
		// 回退：分段模拟流式输出。
		output = b.mockAnswer(req)
	}

	// 3. 分段流式输出最终回答。
	for _, seg := range splitForStream(output, 8) {
		if ctx.Err() != nil {
			return a2a.ExecutionResult{StepID: req.StepID, Status: "killed", Error: "cancelled"}
		}
		reporter.TextDelta(seg)
		time.Sleep(20 * time.Millisecond) // 模拟生成延迟
	}
	reporter.Flush()

	return a2a.ExecutionResult{
		StepID: req.StepID,
		Status: "completed",
		Output: output,
	}
}

// Kill Mock 无子进程，直接返回。
func (b *MockLLMBackend) Kill(ctx context.Context, session a2a.TaskSession) error { return nil }

// callLLM 经 core LLM Proxy 调真实 LLM（OpenAI 兼容 /v1/chat/completions）。
// 缺少 x-llm-proxy-key 或 coreURL 时返回 error，调用方回退模拟。
func (b *MockLLMBackend) callLLM(ctx context.Context, req *a2a.ExecutionRequest) (string, error) {
	key := req.Headers[llm.KeyHeader]
	if b.coreURL == "" || key == "" {
		return "", fmt.Errorf("no llm proxy key or core url")
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":false}`,
		req.Model, req.Input)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.coreURL+"/api/llm-proxy/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(llm.KeyHeader, key)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm proxy http %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	// 极简解析：从 OpenAI 响应取 content（避免引入 JSON 解析依赖，够 mock 用）。
	return extractContent(string(raw)), nil
}

// mockAnswer 生成模拟回答。
func (b *MockLLMBackend) mockAnswer(req *a2a.ExecutionRequest) string {
	return fmt.Sprintf("这是来自 MockLLM 的模拟回答。\n\n你的输入是：%s\n\n（真实 OpenCode 桥接待接入，当前用 mock 验证协议链路）", req.Input)
}

// splitForStream 把文本按每 n 字符切段，用于模拟流式输出。
func splitForStream(s string, n int) []string {
	if n <= 0 {
		return []string{s}
	}
	r := []rune(s)
	parts := make([]string, 0, len(r)/n+1)
	for i := 0; i < len(r); i += n {
		end := i + n
		if end > len(r) {
			end = len(r)
		}
		parts = append(parts, string(r[i:end]))
	}
	return parts
}

// extractContent 从 OpenAI 响应里粗略提取 content 字段（仅 mock 用）。
func extractContent(s string) string {
	idx := strings.Index(s, `"content":"`)
	if idx < 0 {
		return s
	}
	rest := s[idx+len(`"content":"`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return rest
	}
	return rest[:end]
}
