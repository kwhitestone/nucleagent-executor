// watcher.go delegate_task 后台完成的带外续轮监听器。
//
// hermes 的顶层 delegate_task 无条件走后台（run_agent.py _dispatch_delegate_task，
// background 参数已 DEPRECATED/IGNORED）。主代理 turn 1 回复"已派发"后结束，
// 子代理在 daemon 里跑；全部完成后 tui_gateway 的 _notification_poller_loop 触发
// 一次全新的 agent turn（turn 2）送回汇总结果。
//
// 关键约束：turn 2 只在该 session 的 WS 连接存活时触发（断开即 reap session、
// poller 停止）。所以 turn 1 的 Run 结束时**不能**关闭 gateway WS —— 由本 watcher
// 接管连接继续监听。检测到 turn 2 的 message.start 时：
//
//  1. HTTP 通知 core 重建 runState + 签新 TempLLMKey（conversation 回 executing）
//  2. sidecar SetActive(newKey)（turn 2 的 LLM 调用走新 key）
//  3. drain turn 2 事件流，经注入的 ReporterFactory 推给 core
//  4. message.complete 后经 ResultReporter 回报 task_result，core 正常 finalize
//  5. 若 turn 2 又派发了后台任务则回到 1 继续等（链式续轮），否则清理退出
package hermes

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"go.uber.org/zap"
)

// watcherTTL watcher 最长存活时间，对齐 hermes async_delegation 的
// _DURABLE_RETENTION_SECONDS（7 天）：完成事件最长保留这么久，超时后不会再有
// turn 2，watcher 空等没有意义。
const watcherTTL = 7 * 24 * time.Hour

// turnActiveTimeout turn 2 进行中的空闲上限（无事件视为卡死）。等待期（turn
// 未开始）不适用 —— 那本来就可能几天没事件。
const turnActiveTimeout = 5 * time.Minute

// activeWatchers convID -> watcher。新 Run 启动同 conv 时 cancel 旧的
// （用户追问场景：新 Run 的 WS 会接管 session transport，旧 watcher 收不到事件）。
var activeWatchers sync.Map // map[uint]*DelegationWatcher

// HeaderDelegationWatch core 下发的「本轮结束后接管 watcher」信号头。
// 在飞标志持久化在 core DB（conversation.state.delegationPending），executor
// 完全无状态 —— 重启后任何到达该对话的新 Run 都带这个头，watcher 自然接回。
const HeaderDelegationWatch = "x-delegation-watch"

// delegationWatchRequested 判断 core 是否要求本轮结束后接管 watcher。
func delegationWatchRequested(req *a2a.ExecutionRequest) bool {
	v := req.Headers[HeaderDelegationWatch]
	return v != "" && v != "0" && v != "false"
}

// watcherLife watcher 的进程级生命周期（Run 的 ctx 在 Run 结束即取消，watcher
// 的存活跨越 Run 边界，只能挂进程生命周期）。SetWatcherLifetime 由 main 注入。
var watcherLife context.Context = context.Background()

// SetWatcherLifetime 注入进程生命周期 ctx（executor 退出时 watcher 一并退出）。
func SetWatcherLifetime(ctx context.Context) { watcherLife = ctx }

// watchParentCtx Run 移交 watcher 时用的父 ctx。
func watchParentCtx() context.Context { return watcherLife }

// SetWatchHooks 注入带外续轮能力（main 在持久 sender 就绪后调用）。nil 之外的
// 值才会触发 Run 结束时的 WS 移交；nil 时行为与旧版一致（不启用续轮）。
func SetWatchHooks(h *DelegationWatchHooks) {
	conf.WatchHooks = h
}

// DelegationWatchHooks watcher 依赖的外部能力，由 main 在启动期注入
// （hermes 包不能反向 import runtime/wsclient —— 会成环）。
type DelegationWatchHooks struct {
	// StartContinuation 通知 core 开启带外续轮，返回新 key/stepID。
	StartContinuation func(ctx context.Context, convID uint) (key, stepID string, err error)
	// NewReporter 构造 turn 2 的流式上报器（内部绑定持久 sender + stepID）。
	NewReporter func(convID uint, stepID string) a2a.StreamReporter
	// ReportResult 回报 turn 2 最终结果（走 a2a_task_result 通道）。settled=true
	// 表示委托链终结，core 据此清持久化的 delegationPending。
	ReportResult func(convID uint, result a2a.ExecutionResult, settled bool)
}

// DelegationWatcher 单个对话的带外续轮监听器。
type DelegationWatcher struct {
	convID         uint
	sessionID      string
	client         *GatewayClient
	hooks          DelegationWatchHooks
	cancel         context.CancelFunc
	delegatedAgain bool // drainTurn 置位：turn 2 内又 delegate_task（链式续轮）
}

// CancelWatcher 取消指定对话的 watcher（若存在）。新 Run 启动同 conv 前调用。
func CancelWatcher(convID uint) {
	if w, ok := activeWatchers.LoadAndDelete(convID); ok {
		w.(*DelegationWatcher).cancel()
	}
}

// StartDelegationWatcher 启动带外续轮监听。接管 client 的所有权（Run 不再 close）；
// watcher 退出时自行 close。阻塞调用者，需 go 起。
func StartDelegationWatcher(parent context.Context, convID uint, sessionID string, client *GatewayClient, hooks DelegationWatchHooks) {
	CancelWatcher(convID) // 防御：同 conv 旧 watcher（正常不会有）

	// 后台子代理此刻仍在跑，它们的 LLM 调用走 sidecar —— 而 Run 的对话级 key
	// 已被 core 撤销（OnTaskResult RevokeByConversation）。立即换服务级 key
	// （TTL 滑动，永不过期）覆盖，等待期全程有效；turn 2 开始时若 core 下发
	// 对话级 key 再覆盖一次。
	w0 := &DelegationWatcher{convID: convID}
	w0.armServiceKey()

	ctx, cancel := context.WithCancel(context.Background())
	w := &DelegationWatcher{
		convID:    convID,
		sessionID: sessionID,
		client:    client,
		hooks:     hooks,
		cancel:    cancel,
	}
	activeWatchers.Store(convID, w)
	defer func() {
		activeWatchers.Delete(convID)
		client.Close()
	}()

	logInfo("hermes watcher: start", zap.Uint("conv", convID), zap.String("sid", sessionID))
	defer logInfo("hermes watcher: exit", zap.Uint("conv", convID))

	w.run(ctx, parent)
}

// run watcher 主循环：等 turn 2 → drain → 回报 →（链式）再等。
func (w *DelegationWatcher) run(ctx, parentCtx context.Context) {
	ttlTimer := time.NewTimer(watcherTTL)
	defer ttlTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-parentCtx.Done():
			return // executor 进程退出
		case <-ttlTimer.C:
			return // 7 天无 turn 2，放弃
		default:
		}

		started := w.awaitTurnStart(ctx, parentCtx, ttlTimer)
		if !started {
			return
		}

		w.delegatedAgain = false
		if !w.drainTurn(ctx) {
			return
		}
		// turn 2 内若又 delegate_task（drainTurn 置 delegatedAgain），
		// 回到循环顶部继续等下一轮；否则返回结束 watcher。
		if !w.delegatedAgain {
			return
		}
		// 链式续轮：新一轮后台子代理马上要跑 LLM，但 drainTurn 的 defer 已把
		// sidecar key 清了 —— 重新武装服务级 key，等待期全程有效。
		w.armServiceKey()
	}
}

// armServiceKey 用服务级 key 武装 sidecar（等待期后台子代理的 LLM 调用用）。
// 换不到就算了：turn 2 的对话级 key TTL 内可能还活着。
func (w *DelegationWatcher) armServiceKey() {
	if conf.FetchKey == nil || conf.Sidecar == nil {
		return
	}
	if key, err := conf.FetchKey(); err == nil && key != "" {
		conf.Sidecar.SetActive(key)
		logInfo("hermes watcher: service key armed for background subagents",
			zap.Uint("conv", w.convID))
	}
}

func (w *DelegationWatcher) awaitTurnStart(ctx, parentCtx context.Context, ttlTimer *time.Timer) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-parentCtx.Done():
			return false
		case <-ttlTimer.C:
			return false
		case <-w.client.Done():
			return false // WS 断开（hermes 重启等），无路可走
		case evt, ok := <-w.client.Events():
			if !ok {
				return false
			}
			if evt.EventType == evtMessageStart {
				logInfo("hermes watcher: turn 2 start", zap.Uint("conv", w.convID))
				return true
			}
			// 等待期的其他事件（status.update 等）忽略。
		}
	}
}

// drainTurn 处理一个 turn 2：通知 core → SetActive → drain → 回报。
// 返回 false 表示 watcher 应退出（WS 断/TTL）；true 表示可继续等链式续轮。
func (w *DelegationWatcher) drainTurn(ctx context.Context) bool {
	// 1. 通知 core 重建执行上下文。
	if w.hooks.StartContinuation == nil {
		logInfo("hermes watcher: no StartContinuation hook, dropping turn 2", zap.Uint("conv", w.convID))
		return false
	}
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	key, stepID, err := w.hooks.StartContinuation(startCtx, w.convID)
	cancel()
	if err != nil {
		logInfo("hermes watcher: start continuation failed",
			zap.Uint("conv", w.convID), zap.String("err", err.Error()))
		return false
	}
	// core 未下发对话级 key（对话没配 provider/model）→ 与 hermes Run 相同的
	// FetchKey 兜底：走 executor 配置的默认 provider 换服务级 key。
	if key == "" {
		if conf.FetchKey == nil {
			logInfo("hermes watcher: no key and no FetchKey fallback", zap.Uint("conv", w.convID))
			return false
		}
		var kerr error
		key, kerr = conf.FetchKey()
		if kerr != nil || key == "" {
			logInfo("hermes watcher: fetch key fallback failed",
				zap.Uint("conv", w.convID), zap.String("err", kerr.Error()))
			return false
		}
	}

	// 2. 注入新 key（turn 2 的 LLM 调用）。
	if conf.Sidecar != nil {
		conf.Sidecar.SetActive(key)
		defer conf.Sidecar.ClearActive()
	}

	// 3. 构造 reporter，drain 事件。
	var reporter a2a.StreamReporter
	if w.hooks.NewReporter != nil {
		reporter = w.hooks.NewReporter(w.convID, stepID)
	} else {
		reporter = noopStreamReporter{}
	}
	delegatedAgain := false
	output, status, errMsg := w.drainTurnEvents(ctx, w.client, reporter, &delegatedAgain)

	// 4. 回报 task_result。
	result := a2a.ExecutionResult{StepID: stepID, Status: "completed", Output: output}
	if status == "error" || errMsg != "" {
		result = a2a.ExecutionResult{StepID: stepID, Status: "failed", Error: errMsg}
	}
	if w.hooks.ReportResult != nil {
		w.hooks.ReportResult(w.convID, result, !delegatedAgain)
	}

	// 5. 链式：turn 2 又派发了后台任务 → 继续等；否则由上层退出。
	w.delegatedAgain = delegatedAgain
	return true
}

// drainTurnEvents turn 2 的事件循环（语义同 drainEvents，但 message.complete 后
// 不退出 watcher —— 由调用方决定是否继续等链式续轮）。
func (w *DelegationWatcher) drainTurnEvents(ctx context.Context, src eventSource, reporter a2a.StreamReporter, delegatedAgain *bool) (string, string, string) {
	var output strings.Builder
	status := "completed"
	errMsg := ""
	idleTimeout := time.NewTimer(turnActiveTimeout)
	defer idleTimeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return output.String(), "killed", "cancelled"
		case <-idleTimeout.C:
			return output.String(), "failed", "turn 2 idle timeout"
		case evt, ok := <-src.Events():
			idleTimeout.Reset(turnActiveTimeout)
			if !ok {
				if errMsg == "" {
					errMsg = "gateway connection closed before turn 2 completion"
					status = "failed"
				}
				return output.String(), status, errMsg
			}
			switch evt.EventType {
			case evtMessageDelta, evtSubagentText:
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
				reporter.ToolUse(tn, tp)
				if tn == "delegate_task" {
					*delegatedAgain = true
				}
			case evtToolComplete:
				tn := extractToolName(evt.Payload)
				dur := extractToolDuration(evt.Payload)
				reporter.ToolUse(tn, "✓ "+dur)
			case evtMessageComplete:
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
					return output.String(), "error", firstNonEmpty(p.Error, p.Text, "hermes reported turn 2 error")
				}
				logInfo("hermes watcher: turn 2 complete", zap.Uint("conv", w.convID), zap.Int("outputLen", output.Len()))
				return output.String(), status, errMsg
			case evtError:
				var p struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(evt.Payload, &p)
				return output.String(), "error", firstNonEmpty(p.Message, "hermes gateway error (turn 2)")
			}
		}
	}
}

// noopStreamReporter 空 StreamReporter（hooks 未注入时的兜底）。
type noopStreamReporter struct{}

func (noopStreamReporter) TextDelta(string)     {}
func (noopStreamReporter) ThinkingDelta(string) {}
func (noopStreamReporter) Progress(string)      {}
func (noopStreamReporter) ToolUse(string, string) {}
func (noopStreamReporter) Flush()               {}
