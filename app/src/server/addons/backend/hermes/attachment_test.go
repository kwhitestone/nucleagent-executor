package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// TestFetchAttachmentOK 正常下载。
func TestFetchAttachmentOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello-bytes"))
	}))
	defer srv.Close()

	data, err := fetchAttachment(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchAttachment: %v", err)
	}
	if string(data) != "hello-bytes" {
		t.Errorf("data = %q, want hello-bytes", data)
	}
}

// TestFetchAttachmentHTTPError 非 2xx 必须报错，且错误信息里**不能**出现 URL ——
// 签名 URL 带 token，一旦进错误信息就会流进日志。
func TestFetchAttachmentHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := fetchAttachment(context.Background(), srv.URL+"?token=SECRET-TOKEN")
	if err == nil {
		t.Fatal("want error for 403, got nil")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("错误信息泄露了签名 token: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("错误信息应含状态码，实际: %v", err)
	}
}

// TestFetchAttachmentTooLarge 超限必须报错，而不是静默截断成坏文件。
func TestFetchAttachmentTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < (maxAttachmentBytes>>20)+1; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	if _, err := fetchAttachment(context.Background(), srv.URL); err == nil {
		t.Fatal("want error for oversized body, got nil")
	}
}

// TestParseFileRef ref_text 优先，其次 ref_path，最后退回文件名。
func TestParseFileRef(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"ref_text 优先", `{"ref_text":"@file:a.txt","ref_path":"/x/a.txt"}`, "@file:a.txt"},
		{"仅 ref_path", `{"ref_path":"/x/a.txt"}`, "@file:/x/a.txt"},
		{"两者都无则退回文件名", `{"attached":true}`, "fallback.txt"},
		{"坏 JSON 退回文件名", `not json`, "fallback.txt"},
	}
	for _, c := range cases {
		if got := parseFileRef([]byte(c.raw), "fallback.txt"); got != c.want {
			t.Errorf("%s: parseFileRef = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAttachmentName 文件名清洗：只取 basename，空则用 fileId。
func TestAttachmentName(t *testing.T) {
	cases := []struct {
		att  a2a.Attachment
		want string
	}{
		{a2a.Attachment{Name: "a.txt"}, "a.txt"},
		{a2a.Attachment{Name: "  b.txt  "}, "b.txt"},
		// 路径成分必须剥掉：附件名来自用户上传，可能含 ../ 或绝对路径。
		{a2a.Attachment{Name: "../../etc/passwd"}, "passwd"},
		{a2a.Attachment{Name: "dir/sub/c.png"}, "c.png"},
		{a2a.Attachment{Name: `C:\win\d.pdf`}, "d.pdf"},
		{a2a.Attachment{Name: "", FileID: "f-1"}, "f-1"},
	}
	for _, c := range cases {
		if got := attachmentName(c.att); got != c.want {
			t.Errorf("attachmentName(%+v) = %q, want %q", c.att, got, c.want)
		}
	}
}

// TestAttachOneMissingURL 没有 URL 时必须失败（core 签发失败会留空）。
func TestAttachOneMissingURL(t *testing.T) {
	_, err := attachOne(context.Background(), nil, "s1", a2a.Attachment{FileID: "f-1", Name: "a.txt"})
	if err == nil {
		t.Fatal("want error for empty URL, got nil")
	}
}

// TestAttachAllEmpty 无附件时返回空串，不产生任何 prompt 噪音。
func TestAttachAllEmpty(t *testing.T) {
	if got := attachAll(context.Background(), nil, "s1", nil, nil); got != "" {
		t.Errorf("attachAll(nil) = %q, want empty", got)
	}
}

// TestAttachAllAllFailReturnsEmpty 全部附件失败时返回空串（不给 prompt 加一个空的
// "[附件]" 段落），且 reporter 为 nil 也不 panic。
func TestAttachAllAllFailReturnsEmpty(t *testing.T) {
	atts := []a2a.Attachment{{FileID: "f-1", Name: "a.txt"}} // 无 URL，必失败
	if got := attachAll(context.Background(), nil, "s1", atts, nil); got != "" {
		t.Errorf("attachAll = %q, want empty when all attachments fail", got)
	}
}

// TestSessionResetRequested 只有明确为真的值才触发重建 —— 否则一个残留的
// "x-session-reset: 0" 会让每轮都重建 session，白扔掉 resume 的增量优势。
func TestSessionResetRequested(t *testing.T) {
	cases := []struct {
		headers map[string]string
		want    bool
	}{
		{nil, false},
		{map[string]string{}, false},
		{map[string]string{HeaderSessionReset: "1"}, true},
		{map[string]string{HeaderSessionReset: "true"}, true},
		{map[string]string{HeaderSessionReset: "0"}, false},
		{map[string]string{HeaderSessionReset: "false"}, false},
		{map[string]string{HeaderSessionReset: ""}, false},
		{map[string]string{"other": "1"}, false},
	}
	for _, c := range cases {
		got := sessionResetRequested(&a2a.ExecutionRequest{Headers: c.headers})
		if got != c.want {
			t.Errorf("sessionResetRequested(%v) = %v, want %v", c.headers, got, c.want)
		}
	}
}

// TestHistoryAttachmentsMentionedInMessages 历史附件必须在注入 hermes 的 messages 里
// 留下文件名痕迹，否则 session 重建后早期附件对模型完全不可见。
//
// 复刻 resumeOrCreateSession 里的组装逻辑（那段嵌在 RPC 流程里，无法单独调用）。
func TestHistoryAttachmentsMentionedInMessages(t *testing.T) {
	raw, _ := json.Marshal(a2a.ExecutionContext{History: []a2a.HistoryMessage{{
		Role:        "user",
		Content:     "看下这个",
		Attachments: []a2a.Attachment{{FileID: "f-1", Name: "report.pdf", Kind: a2a.AttachmentKindPDF}},
	}}})

	ec, err := a2a.DecodeExecutionContext(raw)
	if err != nil {
		t.Fatalf("DecodeExecutionContext: %v", err)
	}
	h := ec.History[0]
	content := h.Content
	if len(h.Attachments) > 0 {
		names := make([]string, 0, len(h.Attachments))
		for _, a := range h.Attachments {
			names = append(names, attachmentName(a))
		}
		content += "\n[附件: " + strings.Join(names, ", ") + "]"
	}
	if !strings.Contains(content, "report.pdf") {
		t.Errorf("history content = %q, 应含附件文件名", content)
	}
}
