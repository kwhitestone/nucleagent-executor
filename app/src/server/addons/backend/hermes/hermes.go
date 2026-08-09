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
	"sync"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"go.uber.org/zap"

	"whitestone.top/prism-fusion/global"

	"nucleagent-executor/addons/backend"
)

// Capability Hermes 后端能力标识（与 config.yaml 的 nucleagent.backend 匹配）。
const Capability = "hermes"

// Config 由 main 在启动期经 Configure 注入。
type Config struct {
	Bin      string   // hermes 可执行文件
	Workdir  string   // HERMES_HOME
	Host     string   // hermes serve 监听 host
	CoreURL  string   // core API 地址
	Model    string   // LLM 模型名
	Sidecar  *Sidecar // hermes→core LLM Proxy 的本地反代（常驻，按 Run 注入 key）
	FetchKey func() (string, error) // 向 core 换 TempLLMKey（每次 Run 调）
}

// conf 包级配置，由 Configure 注入。
var conf Config

// sessionMap 缓存 ConversationID → stored_session_id，正常对话时用 session.resume
// 增量追加（不全量重发历史）。容器重建后 resume 失败 → fallback 全量注入。
var sessionMap sync.Map // map[uint]string

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

	// 1. 向 core 换本次对话的 TempLLMKey，设进 sidecar（转发时注入）。
	//    hermes 常驻进程只看到 sidecar 的固定地址 + 固定 token，不感知 key 轮换。
	key, keyErr := "", error(nil)
	if conf.FetchKey != nil {
		key, keyErr = conf.FetchKey()
	}
	if keyErr != nil || key == "" {
		return fail(fmt.Sprintf("fetch llm key: %v", keyErr))
	}
	if conf.Sidecar != nil {
		conf.Sidecar.SetActive(key)
	}

	// 2. 确保 hermes 常驻进程就绪。
	global.PRISM_LOG.Info("hermes Run: ensureStarted", zap.Uint("conv", req.ConversationID))
	proc, err := sup.ensureStarted()
	if err != nil {
		return fail(fmt.Sprintf("hermes not ready: %v", err))
	}

	// 3. 连 gateway WS。
	global.PRISM_LOG.Info("hermes Run: dial WS", zap.String("wsUrl", proc.WSURL()))
	client, err := Dial(ctx, proc.WSURL())
	if err != nil {
		return fail(err.Error())
	}
	defer client.Close()
	defer func() {
		if conf.Sidecar != nil {
			conf.Sidecar.ClearActive()
		}
	}()

	// 4. session.resume（增量）或 create + 全量历史注入。
	global.PRISM_LOG.Info("hermes Run: resumeOrCreateSession", zap.Uint("conv", req.ConversationID))
	sessionID, err := resumeOrCreateSession(ctx, client, req)
	if err != nil {
		return fail(err.Error())
	}
	global.PRISM_LOG.Info("hermes Run: session ready", zap.String("sid", sessionID))

	// 5. prompt.submit。
	global.PRISM_LOG.Info("hermes Run: prompt.submit", zap.String("sid", sessionID), zap.Int("inputLen", len(req.Input)))
	if _, err := client.Call(ctx, "prompt.submit", map[string]any{
		"session_id": sessionID,
		"text":       req.Input,
	}); err != nil {
		return fail(fmt.Sprintf("prompt.submit: %v", err))
	}
	global.PRISM_LOG.Info("hermes Run: prompt.submit acked, draining events")

	// 6. 立即发一个思考提示，让用户看到即时反馈（不等第一条 thinking_delta）。
	reporter.ThinkingDelta("正在处理…")

	// 7. 读事件流直到完成/取消。
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

// resumeOrCreateSession 优先 resume（增量），失败则 create + 全量历史注入（从 core 恢复）。
//
// 正常对话（hermes 容器没重建）：session.resume 复用已有 session，只追加新消息，
// 不全量重发历史——省 token、无冗余。
// 容器重建后（state.db 丢失）：resume 失败 → fallback 到 session.create + 把
// core DB 的历史全量注入 messages 参数，从 core 完整恢复。
func resumeOrCreateSession(ctx context.Context, client *GatewayClient, req *a2a.ExecutionRequest) (string, error) {
	// 尝试 resume（正常路径：增量、高效）。
	if stored, ok := sessionMap.Load(req.ConversationID); ok {
		storedID := stored.(string)
		result, err := client.Call(ctx, "session.resume", map[string]any{
			"session_id": storedID,
		})
		if err == nil {
			// resume 成功，从 result 拿新的内存 session_id。
			var resp struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(result, &resp) == nil && resp.SessionID != "" {
				return resp.SessionID, nil
			}
			return storedID, nil
		}
		// resume 失败（session 过期/容器重建）→ fallback。
		sessionMap.Delete(req.ConversationID)
	}

	// Fallback：session.create + 全量历史注入（从 core DB 恢复）。
	params := map[string]any{
		"close_on_disconnect": false, // 保留 session 供下次 resume
		"title":               fmt.Sprintf("nucleagent conv=%d", req.ConversationID),
	}
	if len(req.Context) > 0 {
		var hist []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(req.Context, &hist) == nil && len(hist) > 0 {
			msgs := make([]map[string]string, 0, len(hist))
			for _, h := range hist {
				msgs = append(msgs, map[string]string{"role": h.Role, "content": h.Content})
			}
			params["messages"] = msgs
		}
	}
	result, err := client.Call(ctx, "session.create", params)
	if err != nil {
		return "", fmt.Errorf("session.create: %w", err)
	}
	var resp struct {
		SessionID       string `json:"session_id"`
		StoredSessionID string `json:"stored_session_id"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("session.create: parse: %w", err)
	}
	if resp.SessionID == "" {
		return "", fmt.Errorf("session.create returned no session_id")
	}
	// 缓存 stored_session_id 供下次 resume。
	cacheKey := resp.SessionID
	if resp.StoredSessionID != "" {
		cacheKey = resp.StoredSessionID
	}
	sessionMap.Store(req.ConversationID, cacheKey)
	return resp.SessionID, nil
}

// drainEvents 读事件流，按类型分发到 reporter，直到 message.complete 或 ctx 取消。
// 5 分钟无事件（hermes 工具调用卡住）自动超时，避免永远阻塞。
// 返回 (累积文本, 终态status, 错误消息)。
func drainEvents(ctx context.Context, client *GatewayClient, sessionID string, reporter a2a.StreamReporter) (string, string, string) {
	var output strings.Builder
	status := "completed"
	errMsg := ""
	idleTimeout := time.NewTimer(5 * time.Minute)
	defer idleTimeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return output.String(), "killed", "cancelled"
		case <-idleTimeout.C:
			// hermes 长时间无事件（工具调用卡住/容器内无 browser 等）。
			return output.String(), "failed", "hermes idle timeout (5min no events)"
		case evt, ok := <-client.Events():
			// 收到事件，重置空闲计时器。
			idleTimeout.Reset(5 * time.Minute)
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
				tn := extractToolName(evt.Payload)
				tp := extractToolPreview(evt.Payload)
				global.PRISM_LOG.Info("hermes tool.start", zap.String("tool", tn), zap.String("preview", tp[:min(60,len(tp))]), zap.String("raw_payload", string(evt.Payload)))
				reporter.ToolUse(tn, tp)
			case evtToolComplete:
				tn := extractToolName(evt.Payload)
				dur := extractToolDuration(evt.Payload)
				global.PRISM_LOG.Info("hermes tool.complete", zap.String("tool", tn), zap.String("dur", dur))
				reporter.ToolUse(tn, "✓ "+dur)
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

// extractToolDuration 从 tool.complete 的 payload 取耗时（秒）。
func extractToolDuration(payload json.RawMessage) string {
	var p struct {
		DurationS float64 `json:"duration_s"`
	}
	if json.Unmarshal(payload, &p) == nil && p.DurationS > 0 {
		return fmt.Sprintf("%.1fs", p.DurationS)
	}
	return "done"
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

// extractToolPreview 从 tool 事件的 payload 取有意义的描述。
// hermes tool.start: {"name":"terminal","context":"echo $((1+1))"} → context 是命令
// hermes tool.complete: {"name":"search_files","args":{"path":"/opt"},"result":"..."} → args 是参数
func extractToolPreview(payload json.RawMessage) string {
	// 先试 context（tool.start 的命令/描述）
	var ctx struct {
		Context string `json:"context"`
	}
	if json.Unmarshal(payload, &ctx) == nil && ctx.Context != "" {
		return ctx.Context
	}
	// 再试 args（可能是 string 或 object）
	var raw struct {
		Args    json.RawMessage `json:"args"`
		Preview string          `json:"preview"`
	}
	if json.Unmarshal(payload, &raw) == nil {
		if raw.Preview != "" {
			return raw.Preview
		}
		if len(raw.Args) > 0 && string(raw.Args) != "\"\"" {
			return strings.TrimSpace(string(raw.Args))
		}
	}
	return ""
}

// writeManagedConfig 把 core LLM Proxy 凭据写进 hermes 的 managed 层 config.yaml。
//
// 启动时调一次（Configure 路径），用 core 签发的服务级长效 key。hermes 常驻进程
// 读一次缓存，所有对话复用——key 靠 core 侧 RefreshTTL 滑动续期永不过期。
//
// hermes 的 managed 层（$HERMES_MANAGED_DIR/config.yaml）覆盖 user 配置，且
// provider:custom 时会读 model.api_key。见 agentia-executor-hermes/shell/src/hermes/managed.rs。
func WriteManagedConfig(model, apiKey, baseURL string) error {
	if apiKey == "" {
		return fmt.Errorf("writeManagedConfig: empty api key")
	}
	if model == "" {
		model = "gpt-4o"
	}
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
	global.PRISM_LOG.Info("hermes managed config written",
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
