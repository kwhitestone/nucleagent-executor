package runtime

import (
	"github.com/nucleagent/nucleagent-shared/a2a"
)

// cloudReporter 把 StreamReporter 事件转成 a2a_stream_event 信封发回 core。
//
// 参考附录 §7.1：core 侧对 text_delta 走就地 upsert + 幂等键，
// executor 侧只负责把 delta 及时发出（core 负责合并与 flush 频率控制）。
type cloudReporter struct {
	sender         EnvelopeSender
	conversationID uint
	stepID         string
}

func newCloudReporter(sender EnvelopeSender, conversationID uint, stepID string) *cloudReporter {
	return &cloudReporter{
		sender:         sender,
		conversationID: conversationID,
		stepID:         stepID,
	}
}

func (r *cloudReporter) TextDelta(content string) {
	r.emit("text_delta", content, "", "")
}

func (r *cloudReporter) ThinkingDelta(content string) {
	r.emit("thinking_delta", content, "", "")
}

func (r *cloudReporter) Progress(content string) {
	r.emit("progress", content, "", "")
}

func (r *cloudReporter) ToolUse(tool, content string) {
	r.emit("tool_use", content, tool, "")
}

// Flush cloudReporter 即时发送，无需缓冲 flush。保留接口实现。
func (r *cloudReporter) Flush() {}

// emit 发送一个 a2a_stream_event 信封。
func (r *cloudReporter) emit(eventType, content, tool, _ string) {
	if content == "" && tool == "" {
		return
	}
	_ = r.sender.Send(a2a.EnvA2AStreamEvent, a2a.A2AStreamEventPayload{
		ConversationID: r.conversationID,
		StepID:         r.stepID,
		EventType:      eventType,
		Content:        content,
		Tool:           tool,
	})
}
