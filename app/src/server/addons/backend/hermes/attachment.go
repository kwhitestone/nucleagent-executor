// Package hermes 的附件注入：把 core 下发的附件引用变成 hermes session 上的附件。
//
// 顺序有硬约束：**必须在 prompt.submit 之前 attach**。hermes 把附件挂在 session 上
// 累积，直到 prompt.submit 时才 drain 进当轮对话（见 hermes-agent 的
// _run_prompt_submit）。submit 之后再 attach，本轮看不到，要等下一轮。
//
// 传输方式选 base64 而非本地路径：hermes 的 file.attach / image.attach_bytes 支持
// content_base64 / data_url，不要求与 executor 共享文件系统。这样 hermes 换成
// 远程 gateway 或独立容器时，这段代码不用改。
package hermes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"go.uber.org/zap"
	"github.com/kwhitestone/prism-fusion/global"
)

// maxAttachmentBytes 单个附件的下载上限（20MB）。
//
// 比 storage 的 100MB 上限低：附件要 base64（膨胀 33%）后经 JSON-RPC 传给 hermes，
// 再进模型上下文。真正的瓶颈是后两者，不是存储。
const maxAttachmentBytes = 20 << 20

// attachmentHTTPTimeout 单个附件的下载超时。
const attachmentHTTPTimeout = 60 * time.Second

// attachClient 拉取附件字节的 HTTP 客户端。
//
// 显式关掉代理：宿主机常设 HTTP_PROXY，走代理会让 CDN 直链下载失败或绕远路。
var attachClient = &http.Client{
	Timeout:   attachmentHTTPTimeout,
	Transport: &http.Transport{Proxy: nil},
}

// warnAttach / logDebug / logInfo 都是 global.PRISM_LOG 的 nil 守卫包装。
//
// 框架只在启动后才给 global.PRISM_LOG 赋值；单测不初始化它时直接调会 nil panic。
// 尤其 drainEvents 的 Debug 在每条事件上都打 —— 必须走守卫。
func warnAttach(msg string, fields ...zap.Field) {
	if global.PRISM_LOG == nil {
		return
	}
	global.PRISM_LOG.Warn(msg, fields...)
}

func logDebug(msg string, fields ...zap.Field) {
	if global.PRISM_LOG == nil {
		return
	}
	global.PRISM_LOG.Debug(msg, fields...)
}

func logInfo(msg string, fields ...zap.Field) {
	if global.PRISM_LOG == nil {
		return
	}
	global.PRISM_LOG.Info(msg, fields...)
}

// attachAll 把附件逐个注入 session，返回需要追加到 prompt 的引用文本。
//
// 单个附件失败不中断整轮：记日志 + 通过 reporter 告知用户，继续处理其余附件。
// 附件通常是补充材料，因为一个文件拉不到就让整个任务失败，代价过高。
func attachAll(ctx context.Context, client *GatewayClient, sessionID string,
	atts []a2a.Attachment, reporter a2a.StreamReporter) string {
	if len(atts) == 0 {
		return ""
	}

	var refs []string
	for _, att := range atts {
		ref, err := attachOne(ctx, client, sessionID, att)
		if err != nil {
			warnAttach("hermes: 附件注入失败",
				zap.String("fileId", att.FileID), zap.String("name", att.Name), zap.Error(err))
			if reporter != nil {
				reporter.Progress(fmt.Sprintf("附件 %s 处理失败，已跳过", att.Name))
			}
			continue
		}
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return ""
	}
	// 非图片类附件必须把 @file: 引用写进 prompt，否则 agent 不知道文件存在 ——
	// 文件只是躺在工作区里，模型没有任何线索去读它。
	return "\n\n[附件]\n" + strings.Join(refs, "\n")
}

// attachOne 注入单个附件，返回 prompt 引用文本（图片/PDF 返回空串，它们直接进视觉上下文）。
func attachOne(ctx context.Context, client *GatewayClient, sessionID string, att a2a.Attachment) (string, error) {
	if att.URL == "" {
		// core 侧签发失败时会留空 URL（见 core 的 signAttachments）。
		return "", fmt.Errorf("附件缺少下载地址")
	}
	data, err := fetchAttachment(ctx, att.URL)
	if err != nil {
		return "", err
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	name := attachmentName(att)

	switch att.Kind {
	case a2a.AttachmentKindImage:
		_, err = client.Call(ctx, "image.attach_bytes", map[string]any{
			"session_id":     sessionID,
			"content_base64": b64,
			"filename":       name,
			"ext":            strings.TrimPrefix(strings.ToLower(path.Ext(name)), "."),
		})
		return "", err

	case a2a.AttachmentKindPDF:
		_, err = client.Call(ctx, "pdf.attach", map[string]any{
			"session_id":     sessionID,
			"content_base64": b64,
			"filename":       name,
		})
		return "", err

	default:
		// file.attach 走 data_url（不依赖共享文件系统），返回 @file: 引用。
		mime := att.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		res, err := client.Call(ctx, "file.attach", map[string]any{
			"session_id": sessionID,
			"data_url":   "data:" + mime + ";base64," + b64,
			"name":       name,
		})
		if err != nil {
			return "", err
		}
		return parseFileRef(res, name), nil
	}
}

// parseFileRef 从 file.attach 响应里取 ref_text（形如 "@file:xxx"）。
//
// 取不到时退回文件名：宁可给模型一个不精确的提示，也好过完全不提这个附件。
func parseFileRef(raw []byte, fallbackName string) string {
	var resp struct {
		RefText string `json:"ref_text"`
		RefPath string `json:"ref_path"`
	}
	if err := json.Unmarshal(raw, &resp); err == nil {
		if resp.RefText != "" {
			return resp.RefText
		}
		if resp.RefPath != "" {
			return "@file:" + resp.RefPath
		}
	}
	return fallbackName
}

// attachmentName 取一个安全的展示/存储文件名。
func attachmentName(att a2a.Attachment) string {
	name := strings.TrimSpace(att.Name)
	if name == "" {
		name = att.FileID
	}
	// 只取 basename：附件名来自用户上传，可能含路径分隔符。
	return path.Base(strings.ReplaceAll(name, "\\", "/"))
}

// fetchAttachment 下载附件字节，带大小上限。
func fetchAttachment(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := attachClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载附件失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 不回显 URL：它带签名 token。
		return nil, fmt.Errorf("下载附件返回 %d", resp.StatusCode)
	}
	// LimitReader 多读 1 字节用于判断是否超限（而不是静默截断成坏文件）。
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取附件失败: %w", err)
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("附件超过 %dMB 上限", maxAttachmentBytes>>20)
	}
	return data, nil
}
