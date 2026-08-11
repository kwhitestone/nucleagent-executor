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
	"github.com/nucleagent/nucleagent-shared/llm"
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

// HeaderSessionReset core 要求丢弃 hermes 侧已缓存 session 的信号头。
//
// 走 Headers 而不是新增 ExecutionRequest 字段：这是「本次执行的一个指令」，
// 不是任务数据；且旧版 executor 收到未知头会自然忽略，无需协议协商。
const HeaderSessionReset = "x-session-reset"

// sessionResetRequested 判断 core 是否要求重建 session。
func sessionResetRequested(req *a2a.ExecutionRequest) bool {
	v := req.Headers[HeaderSessionReset]
	return v != "" && v != "0" && v != "false"
}

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

	// 1. 取本次执行用的 TempLLMKey，设进 sidecar（转发时注入）。
	//    hermes 常驻进程只看到 sidecar 的固定地址 + 固定 token，不感知 key 轮换。
	//
	//    **优先用 core 随请求下发的对话级 key**（req.Headers）：它绑定了本对话选定的
	//    provider/model，是模型选择能生效的前提；也让 CallLog 能归因到具体对话
	//    （服务级 key 的 ConversationID 恒为 0，那些日志全部无法归因）。
	//    取不到才回退服务级长效 key —— core 未下发或旧版 core 的场景仍可用。
	key := req.Headers[llm.KeyHeader]
	if key == "" {
		if conf.FetchKey == nil {
			return fail("no llm key: core 未下发且未配置服务级 key")
		}
		var keyErr error
		key, keyErr = conf.FetchKey()
		if keyErr != nil || key == "" {
			return fail(fmt.Sprintf("fetch llm key: %v", keyErr))
		}
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

	// 5. 立即发思考提示（在 prompt.submit 之前，用户发消息后马上看到反馈）。
	reporter.ThinkingDelta("正在处理…")

	// 5.5 注入本轮附件。**必须在 prompt.submit 之前**：hermes 在 submit 时才把
	//     session 上累积的附件 drain 进当轮，晚于 submit 则本轮不可见。
	//     非图片附件会返回 @file: 引用，追加到 prompt 让 agent 知道去读。
	prompt := req.Input
	if len(req.Attachments) > 0 {
		global.PRISM_LOG.Info("hermes Run: attach",
			zap.String("sid", sessionID), zap.Int("count", len(req.Attachments)))
		prompt += attachAll(ctx, client, sessionID, req.Attachments, reporter)
	}

	// 6. prompt.submit（fire-and-forget：不等 ack，立即开始读事件流）。
	//    hermes 的 prompt.submit ack 可能和首批事件同时到达，阻塞等 ack 会
	//    延迟事件处理（用户看到"卡住"）。用 Send 发送后立即 drainEvents。
	global.PRISM_LOG.Info("hermes Run: prompt.submit (fire-and-forget)", zap.String("sid", sessionID), zap.Int("inputLen", len(prompt)))
	if err := client.Send("prompt.submit", map[string]any{
		"session_id": sessionID,
		"text":       prompt,
	}); err != nil {
		return fail(fmt.Sprintf("prompt.submit: %v", err))
	}
	global.PRISM_LOG.Info("hermes Run: prompt.submit sent, draining events")

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
	// 模型/provider 变更时 core 会要求重建 session：hermes 的模型是建 session 时
	// 定的，resume 只会继续用旧模型，用户改了模型却毫无变化。
	// 丢掉缓存强制走下面的 create 分支；历史随 req.Context 全量重注，不丢上下文。
	if sessionResetRequested(req) {
		if _, had := sessionMap.LoadAndDelete(req.ConversationID); had {
			global.PRISM_LOG.Info("hermes: 按 core 要求重建 session（模型变更）",
				zap.Uint("conv", req.ConversationID), zap.String("model", req.Model))
		}
	}

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
	// 会话级模型覆盖。hermes 把它作为 PER-SESSION override 处理（不写全局配置），
	// 所以并发的不同对话可以各用各的模型。
	//
	// 只在 create 分支设置：hermes 的模型是建 session 时定的，resume 改不了 ——
	// 这也是 core 换模型时必须让我们重建 session 的原因（见 sessionResetRequested）。
	// 留空则由 hermes 用 managed config 里的默认模型兜底。
	if req.Model != "" {
		params["model"] = req.Model
	}
	// 历史注入。DecodeExecutionContext 兼容两种形态（对象 / 旧的裸数组），
	// 故 core 与 executor 不必同步部署 —— 详见 a2a.DecodeExecutionContext。
	if ec, err := a2a.DecodeExecutionContext(req.Context); err != nil {
		// 解析失败不阻断：没历史也能跑（等于新会话），比整轮失败好。
		global.PRISM_LOG.Warn("hermes: 解析对话历史失败，按无历史继续", zap.Error(err))
	} else if ec != nil && len(ec.History) > 0 {
		msgs := make([]map[string]string, 0, len(ec.History))
		for _, h := range ec.History {
			content := h.Content
			// 历史附件在这里只按文件名提及，不重新 attach 字节。
			//
			// 正常多轮走的是上面的 session.resume 分支 —— hermes 自己保留着
			// session，第 1 轮 attach 的文件仍在其对话历史里，agent 照样能读。
			// 只有 resume 失败回退到 create 时（容器重建/session 过期）才走到这，
			// 此时字节确实不在 hermes 侧了，模型只知道"那轮有这些文件"而读不到内容。
			//
			// 没有在这里重新 attach 是刻意的：附件是挂到 session 上、由下一次
			// prompt.submit drain 进**当轮**的，把历史图片重新 attach 会让它们
			// 混进当前这一轮的视觉上下文，语义错乱。要彻底修得让 file 类附件走
			// 工作区暂存（file.attach 是持久化的，不参与 drain），属独立改进。
			if len(h.Attachments) > 0 {
				names := make([]string, 0, len(h.Attachments))
				for _, a := range h.Attachments {
					names = append(names, attachmentName(a))
				}
				content += "\n[附件: " + strings.Join(names, ", ") + "]"
			}
			msgs = append(msgs, map[string]string{"role": h.Role, "content": content})
		}
		params["messages"] = msgs
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

// tailDelegationLogs 轮询 <workdir>/cache/delegation/live/deleg_*/task-*.log，
// 把子代理的 assistant 输出行通过 reporter.TextDelta 推给前端。
// 不改 hermes 源码——利用 hermes 内置的 delegation live transcript 文件。
// 格式：每行 "HH:MM:SS role     | text"，role=assistant/final/start/think。
func tailDelegationLogs(ctx context.Context, reporter a2a.StreamReporter) {
	globPattern := filepath.Join(conf.Workdir, "cache", "delegation", "live", "deleg_*", "task-*.log")
	global.PRISM_LOG.Info("hermes tailDelegationLogs started", zap.String("glob", globPattern))
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	offsets := make(map[string]int) // file path → read offset
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			files, _ := filepath.Glob(globPattern)
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				off := offsets[f]
				if off >= len(data) {
					continue
				}
				newData := data[off:]
				offsets[f] = len(data)
				// 解析新增行，提取 assistant/final 行推给前端
				for _, line := range strings.Split(string(newData), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					// 格式 "HH:MM:SS role     | text"
					// 用 ThinkingDelta（独立 streaming 行，不干扰主代理输出）
					if strings.Contains(line, " assistant |") || strings.Contains(line, " think    |") {
						text := extractAfterPipe(line)
						if text != "" {
							reporter.ThinkingDelta("[子代理] " + text + "\n")
						}
					} else if strings.Contains(line, " final    |") {
						text := extractAfterPipe(line)
						if text != "" && strings.Contains(text, "summary:") {
							reporter.ThinkingDelta("[完成] " + text + "\n")
						}
					}
				}
			}
		}
	}
}

// extractAfterPipe 从 "HH:MM:SS role | text" 提取 text 部分。
func extractAfterPipe(line string) string {
	idx := strings.Index(line, "| ")
	if idx < 0 {
		return ""
	}
	return line[idx+2:]
}

// eventSource 抽象 drainEvents 对 gateway 客户端的依赖：只需「读事件」和「连接是否关闭」
// 两个能力。抽出来是为了能单测 drainEvents（见 hermes_test.go 的 TestDrainEventsReturnsOnComplete）。
// *GatewayClient 满足它。
type eventSource interface {
	Events() <-chan GatewayEvent
	Done() <-chan struct{}
}

// drainEvents 读事件流，按类型分发到 reporter，直到 message.complete 或 ctx 取消。
//
// 终态判定原则：收到 message.complete 即返回 —— hermes 说这轮生成结束了，那这轮
// 就结束。此前这里曾无条件在 complete 后再等 30s「等子代理汇总轮」，但查 hermes
// 源码后确认这是错的：
//   - 无 delegate_task：纯白等，徒增 30s 窗口（用户答完就追问会被 core 的
//     executing 守卫拒成 409）。
//   - delegate_task 默认（同步，delegate_tool.py:2823 的 background 默认 false）：
//     _execute_and_aggregate 会 join 所有子代理，聚合结果作为 tool result 返回给
//     主代理，主代理再生成最终回答。整轮只有一个 message.complete，且它在子代理
//     全部完成之后。complete 后等不到任何东西。
//   - delegate_task 异步（background=true）：子代理结果走 async_delegation 的持久
//     化 SQLite 投递队列（跨重启重试、保留 7 天），通过 gateway.wake 的 self-POST
//     开一个全新的 turn 回到会话。那不属于本次 prompt.submit 的生命周期，30s 内
//     根本等不到 —— hermes 自己也明确否定了这种 wall-clock 超时（async_delegation.py:
//     "must never be killed for taking long"）。
//
// 故正确的边界就是 message.complete。本函数只剩两类出口：complete 到达 / 5min
// 无事件（工具调用真卡死）。
//
// 已知缺口：background=true 异步子代理完成时，新 turn 会回到一个 OnTaskResult
// 已 delete(running) 的 core —— 那份结果目前会被丢弃。30s 等待从没解决过它；
// 要支持得让 core 能接受「对话的带外新消息」，属独立工作。
func drainEvents(ctx context.Context, src eventSource, sessionID string, reporter a2a.StreamReporter) (string, string, string) {
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
			// 5min 无事件 = hermes 真卡住了（工具调用挂起等）。不是 complete 后的
			// 续传窗口 —— 那个曾经由 30s 计时器处理，现已删（见函数注释）。
			return output.String(), "failed", "hermes idle timeout (5min no events)"
		case evt, ok := <-src.Events():
			// 收到事件即重置空闲计时器 —— 仍处于同一轮生成中。
			idleTimeout.Reset(5 * time.Minute)
			if !ok {
				// 事件流关闭（连接断开）且没收到 complete：视为失败。
				if errMsg == "" {
					errMsg = "gateway connection closed before completion"
					status = "failed"
				}
				return output.String(), status, errMsg
			}
			// 子代理（subagent/delegate）的事件用 child session id 推送，
			// 和主 sessionID 不同——不能过滤掉，否则看不到子代理的并行进度。
			// gateway 是单连接的（每个 Dial 一个独立 WS），不会多路复用。
			// 调试：打印所有事件类型
			logDebug("hermes event", zap.String("type", evt.EventType), zap.Int("payloadLen", len(evt.Payload)))
			switch evt.EventType {
			case evtMessageDelta, evtSubagentText:
				// subagent.text 是子代理的流式输出，和 message.delta 一样处理
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
				logInfo("hermes tool.start", zap.String("tool", tn))
				reporter.ToolUse(tn, tp)
				// delegate_task 启动子代理——hermes 的 daemon 线程写 live transcript 到
				// <workdir>/cache/delegation/live/deleg_*/task-*.log，但 WS 不推送。
				// 启动 tail goroutine 读 log 文件，把子代理输出推给前端。
				if tn == "delegate_task" {
					tailCtx, tailCancel := context.WithCancel(ctx)
					go tailDelegationLogs(tailCtx, reporter)
					// 在 tool.complete 或 drainEvents 结束时 cancel
					defer tailCancel()
				}
			case evtToolComplete:
				tn := extractToolName(evt.Payload)
				dur := extractToolDuration(evt.Payload)
				logInfo("hermes tool.complete", zap.String("tool", tn), zap.String("dur", dur))
				reporter.ToolUse(tn, "✓ "+dur)
			case evtMessageComplete:
				// complete 带完整 text，优先于增量累积。
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
					errMsg = firstNonEmpty(p.Error, p.Text, "hermes reported message error")
					return output.String(), status, errMsg
				}
				// complete 即本轮终结 —— 直接返回。
				// 详见函数头注释：delegate_task 的同步/异步两条路径都不需要在 complete
				// 后再等；那个曾经的 30s 窗口既白等（无子代理/同步子代理），又必然不够
				// （异步走持久队列 + 新 turn），还把「答完即追问」误判成 409。
				logInfo("hermes message.complete, returning",
					zap.String("sid", sessionID), zap.Int("outputLen", output.Len()))
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
		case <-src.Done():
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
