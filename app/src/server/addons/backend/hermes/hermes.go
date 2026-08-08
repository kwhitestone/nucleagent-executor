// Package hermes 的 Backend 实现：把 core 下发的 ExecutionRequest 转成
// `hermes serve` 的 WebSocket JSON-RPC 调用，把流式事件回报给 reporter。
//
// 一次 Run 的流程：
//  1. ensureStarted() 确保 hermes serve 常驻进程已就绪（懒启动，单例）。
//  2. writeManagedConfig() 把 core LLM Proxy 的 base_url/api_key/model 写进
//     $HERMES_MANAGED_DIR/config.yaml（hermes 的 managed 层，provider=custom）。
//  3. Dial gateway WS，session.create 拿 session_id。
//  4. prompt.submit(req.Input) 触发异步生成。
//  5. 读 event 流：message.delta→TextDelta，thinking/reasoning.delta→
//     ThinkingDelta，tool.*→ToolUse，直到 message.complete 或 ctx 取消。
package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"github.com/nucleagent/nucleagent-shared/llm"
	"go.uber.org/zap"

	"whitestone.top/prism-fusion/global"

	"nucleagent-executor/addons/backend"
)

// Capability Hermes 后端能力标识（与 config.yaml 的 nucleagent.backend 匹配）。
const Capability = "hermes"

// Config 由 main 在启动期经 Configure 注入（与 runtime.SetSender 同构）。
type Config struct {
	Bin     string // hermes 可执行文件
	Workdir string // HERMES_HOME
	Host    string // hermes serve 监听 host
	CoreURL string // core API 地址（拼 LLM Proxy base_url）
}

// conf 包级配置，由 Configure 注入。HermesBackend 的方法都读它。
var conf Config

// Configure 注入 Hermes 配置。由 main 在 runExecutor 里、NewRunner 之前调用。
func Configure(c Config) { conf = c }

// HermesBackend Hermes Agent 执行后端。
type HermesBackend struct{}

func init() {
	backend.Default.Register(&HermesBackend{})
}

// Capability 返回能力标识。
func (b *HermesBackend) Capability() string { return Capability }

// Descriptor 握手时上报给 core 的自描述。
func (b *HermesBackend) Descriptor() a2a.DesktopExecutor {
	return a2a.DesktopExecutor{
		ID:          Capability,
		Type:        "hermes",
		DisplayName: "Hermes Agent",
		Streaming:   true,
	}
}

// Run 执行一次任务：启动/复用 hermes 进程 → 写 managed 配置 → 发 prompt →
// 流式回报。
func (b *HermesBackend) Run(ctx context.Context, req *a2a.ExecutionRequest, reporter a2a.StreamReporter) a2a.ExecutionResult {
	fail := func(msg string) a2a.ExecutionResult {
		return a2a.ExecutionResult{StepID: req.StepID, Status: "failed", Error: msg}
	}

	// 1. 先写 managed 配置（LLM 凭据每次 Run 都写——临时 key 会轮换）。
	if err := writeManagedConfig(req); err != nil {
		return fail(fmt.Sprintf("write managed config: %v", err))
	}

	// 2. 重启 hermes 进程（杀旧启新），让它读取刚写好的 managed config。
	// hermes 在 agent init 缓存 provider+api_key，不重启会复用首个 key。
	proc, err := sup.startFresh()
	if err != nil {
		return fail(fmt.Sprintf("hermes not ready: %v", err))
	}

	// 3. 连 gateway WS。
	client, err := Dial(ctx, proc.WSURL())
	if err != nil {
		return fail(err.Error())
	}
	defer client.Close()

	// 4. session.create。
	sessionID, err := createSession(ctx, client, req)
	if err != nil {
		return fail(err.Error())
	}

	// 5. prompt.submit（异步；输出走事件流）。
	if _, err := client.Call(ctx, "prompt.submit", map[string]any{
		"session_id": sessionID,
		"text":       req.Input,
	}); err != nil {
		return fail(fmt.Sprintf("prompt.submit: %v", err))
	}

	// 6. 读事件流直到完成/取消。
	output, status, errMsg := drainEvents(ctx, client, sessionID, reporter)
	reporter.Flush()

	if ctx.Err() != nil {
		return a2a.ExecutionResult{StepID: req.StepID, Status: "killed", Error: "cancelled"}
	}
	if status == "error" || errMsg != "" {
		return a2a.ExecutionResult{StepID: req.StepID, Status: "failed", Error: errMsg}
	}
	return a2a.ExecutionResult{StepID: req.StepID, Status: "completed", Output: output}
}

// Kill 终止指定会话。hermes 进程本身常驻，Kill 由 runtime 的 ctx 取消触发
// （Run 的 drainEvents 会感知 ctx.Done 返回 killed）；这里无需额外动作。
func (b *HermesBackend) Kill(ctx context.Context, session a2a.TaskSession) error { return nil }

// createSession 调 session.create，返回 session_id。
//
// 注意：不传 model 参数。传 model 会设 model_override，使 hermes 跳过
// _sync_agent_model_with_config（client.rs:224-227），从而不读 managed config
// 里的 model.api_key —— 那是 LLM proxy 临时 key 的唯一注入点。model + api_key
// 都已写在 managed config.yaml，让 hermes 读它即可。
func createSession(ctx context.Context, client *GatewayClient, req *a2a.ExecutionRequest) (string, error) {
	params := map[string]any{
		"close_on_disconnect": true,
		"title":               fmt.Sprintf("nucleagent conv=%d step=%s", req.ConversationID, req.StepID),
	}

	result, err := client.Call(ctx, "session.create", params)
	if err != nil {
		return "", fmt.Errorf("session.create: %w", err)
	}
	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("session.create: parse: %w", err)
	}
	if resp.SessionID == "" {
		return "", fmt.Errorf("session.create returned no session_id")
	}
	return resp.SessionID, nil
}

// drainEvents 读事件流，按类型分发到 reporter，直到 message.complete 或 ctx 取消。
// 返回 (累积文本, 终态status, 错误消息)。
func drainEvents(ctx context.Context, client *GatewayClient, sessionID string, reporter a2a.StreamReporter) (string, string, string) {
	var output strings.Builder
	status := "completed"
	errMsg := ""

	for {
		select {
		case <-ctx.Done():
			return output.String(), "killed", "cancelled"
		case evt, ok := <-client.Events():
			if !ok {
				// 事件流关闭（连接断开）且没收到 complete：视为失败。
				if errMsg == "" {
					errMsg = "gateway connection closed before completion"
					status = "failed"
				}
				return output.String(), status, errMsg
			}
			// 只处理本 session 的事件（gateway 可能多路复用）。
			if evt.SessionID != "" && evt.SessionID != sessionID {
				continue
			}
			switch evt.EventType {
			case evtMessageDelta:
				if t := extractText(evt.Payload); t != "" {
					output.WriteString(t)
					reporter.TextDelta(t)
				}
			case evtThinkingDelta, evtReasoningDelta:
				if t := extractText(evt.Payload); t != "" {
					reporter.ThinkingDelta(t)
				}
			case evtToolStart:
				reporter.ToolUse(extractToolName(evt.Payload), extractToolPreview(evt.Payload))
			case evtToolComplete:
				reporter.ToolUse(extractToolName(evt.Payload), "done")
			case evtMessageComplete:
				// 终态：complete 带完整 text，优先于增量累积。
				// hermes 的 complete payload 是 {text, usage, status}；失败时诊断
				// 走 text 而非 error 字段（对齐 shell/src/hermes/mod.rs:288-297）。
				var p struct {
					Text   string `json:"text"`
					Status string `json:"status"`
					Error  string `json:"error"`
				}
				_ = json.Unmarshal(evt.Payload, &p)
				if strings.TrimSpace(p.Text) != "" {
					output.Reset()
					output.WriteString(p.Text)
				}
				if p.Status == "error" || p.Error != "" {
					status = "error"
					// 失败但无显式 error：用 text 作诊断，避免空错误信息。
					errMsg = firstNonEmpty(p.Error, p.Text, "hermes reported message error")
				}
				return output.String(), status, errMsg
			case evtError:
				var p struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(evt.Payload, &p)
				return output.String(), "error", firstNonEmpty(p.Message, "hermes gateway error")
			case evtGatewayReady, evtMessageStart:
				// 生命周期信号，无操作。
			}
		case <-client.Done():
			if errMsg == "" {
				errMsg = "gateway connection closed before completion"
				status = "failed"
			}
			return output.String(), status, errMsg
		}
	}
}

// extractToolName 从 tool.start/complete 的 payload 取工具名。
func extractToolName(payload json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
		Tool string `json:"tool"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.Name != "" {
		return p.Name
	}
	return p.Tool
}

// extractToolPreview 从 tool.start 的 payload 取预览文本。
func extractToolPreview(payload json.RawMessage) string {
	var p struct {
		Preview string `json:"preview"`
		Args    string `json:"args"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.Preview != "" {
		return p.Preview
	}
	return p.Args
}

// writeManagedConfig 把 core LLM Proxy 凭据写进 hermes 的 managed 层 config.yaml。
//
// hermes 的 managed 层（$HERMES_MANAGED_DIR/config.yaml）覆盖 user 配置，且
// provider:custom 时会读 model.api_key（registry provider 会忽略 api_key 强制
// 走环境变量）。见 agentia-executor-hermes/shell/src/hermes/managed.rs:48-65。
func writeManagedConfig(req *a2a.ExecutionRequest) error {
	apiKey := req.Headers[llm.KeyHeader]
	if apiKey == "" {
		return fmt.Errorf("missing %s header", llm.KeyHeader)
	}
	model := req.Model
	if model == "" {
		model = "gpt-4o" // 兜底；正常应由 core 下发 req.Model
	}
	baseURL := strings.TrimRight(conf.CoreURL, "/") + "/api/llm-proxy/v1"

	managedDir := filepath.Join(conf.Workdir, "managed")
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("model:\n  default: %s\n  provider: custom\n  base_url: %s\n  api_key: %s\napprovals:\n  mode: \"off\"\n",
		yamlScalar(model), yamlScalar(baseURL), yamlScalar(apiKey))
	target := filepath.Join(managedDir, "config.yaml")
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		return err
	}
	global.PRISM_LOG.Debug("hermes managed config written",
		zap.String("model", model), zap.String("base_url", baseURL), zap.String("path", target))
	return nil
}

// yamlScalar 把字符串转成 YAML 单行标量：含特殊字符（:#'"{[]} 等）或空则
// 双引号包裹并转义内部双引号；否则裸用。对齐 managed.rs 的 yaml_scalar 行为。
func yamlScalar(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	if s == "" || strings.ContainsAny(s, ":#'\"{}[],&*?|<>=!%@`\n") {
		quoted := strings.ReplaceAll(s, `"`, `\"`)
		return "\"" + quoted + "\""
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
