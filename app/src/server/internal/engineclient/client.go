// Package engineclient 封装 executor -> core 的 HTTP 注册。
//
// executor 启动后向 core 发起注册（携带 ExecutorToken），core 返回 wsUrl，
// executor 随后用 wsUrl 建立 WebSocket。注册失败时按间隔重试。
package engineclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

// Client core 注册客户端。
type Client struct {
	coreURL          string
	executorToken    string
	deviceID         string
	instanceID       string
	deviceName       string
	http             *http.Client
}

// NewClient 构造注册客户端。
func NewClient(coreURL, executorToken, deviceID, instanceID, deviceName string) *Client {
	return &Client{
		coreURL:       coreURL,
		executorToken: executorToken,
		deviceID:      deviceID,
		instanceID:    instanceID,
		deviceName:    deviceName,
		// 显式不走系统代理：executor → core 是 localhost 互调，若宿主机有
		// HTTP_PROXY 环境变量，Go 默认 Transport 会把请求发到代理端口导致 502。
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
}

// RegisterRequest 注册请求体（对齐 core s2s addon 期望）。
type RegisterRequest struct {
	DeviceID     string   `json:"deviceId"`
	InstanceID   string   `json:"instanceId,omitempty"`
	DeviceName   string   `json:"deviceName,omitempty"`
	BackendType  string   `json:"backendType,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Token        string   `json:"-"` // 走 header，不落 body
}

// RegisterResponse 注册响应体。WSURL 是 core 下发的 WebSocket 地址。
type RegisterResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		WSURL     string    `json:"wsUrl"`
		DeviceID  string    `json:"deviceId,omitempty"`
		ExpiresAt time.Time `json:"expiresAt,omitempty"`
	} `json:"data"`
}

// Register 发起注册，返回 wsUrl。retryUntil 在 ctx 取消前按间隔重试。
func (c *Client) Register(ctx context.Context, capabilities []string, retry time.Duration) (string, error) {
	if c.executorToken == "" {
		return "", fmt.Errorf("engineclient: EXECUTOR_TOKEN not set")
	}
	body, err := json.Marshal(RegisterRequest{
		DeviceID:     c.deviceID,
		InstanceID:   c.instanceID,
		DeviceName:   c.deviceName,
		Capabilities: capabilities,
	})
	if err != nil {
		return "", err
	}
	url := c.coreURL + "/api/v1/addons/s2s/executor/register"

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("engineclient: register cancelled: %w (last err: %v)", ctx.Err(), lastErr)
			}
			return "", ctx.Err()
		default:
		}

		wsURL, err := c.tryRegister(ctx, url, body)
		if err == nil {
			return wsURL, nil
		}
		fmt.Fprintf(os.Stderr, "engineclient: register attempt failed (retry in %v): %v\n", retry, err)
		lastErr = err
		// 等待重试间隔或 ctx 取消。
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return "", fmt.Errorf("engineclient: register cancelled: %w (last err: %v)", ctx.Err(), lastErr)
		case <-t.C:
		}
	}
}

// tryRegister 执行单次注册请求。
func (c *Client) tryRegister(ctx context.Context, url string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Executor-Token", c.executorToken)
	req.Header.Set("X-Request-ID", uuid.NewString())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("register request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read register response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var out RegisterResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("register unmarshal: %w", err)
	}
	if out.Code != 0 || out.Data.WSURL == "" {
		return "", fmt.Errorf("register rejected: code=%d msg=%s", out.Code, out.Message)
	}
	return out.Data.WSURL, nil
}

// AsyncContinuationResponse core 的 async-continuation/start 端点响应。
type AsyncContinuationResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Key          string `json:"key"`
		StepID       string `json:"stepId"`
		DelegationID string `json:"delegationId"`
		SenderSlug   string `json:"senderSlug"`
	} `json:"data"`
}

// StartAsyncContinuation 通知 core 开启带外续轮（delegate_task 后台完成后的
// 汇总 turn）。core 重建 runState + 签新 TempLLMKey，返回给 watcher 用于
// 注入 sidecar 和回报事件流。
func (c *Client) StartAsyncContinuation(ctx context.Context, conversationID uint) (*AsyncContinuationResponse, error) {
	body, _ := json.Marshal(map[string]any{"conversationId": conversationID})
	url := c.coreURL + "/api/v1/addons/s2s/executor/async-continuation/start"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Executor-Token", c.executorToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("async-continuation request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("async-continuation http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out AsyncContinuationResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("async-continuation unmarshal: %w", err)
	}
	if out.Code != 0 || out.Data.StepID == "" {
		return nil, fmt.Errorf("async-continuation rejected: code=%d msg=%s", out.Code, out.Message)
	}
	return &out, nil
}

// truncate 截断字符串到 max 字节，超出加省略号。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// LLMKeyResponse core 的 llm-key 端点响应。
type LLMKeyResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Key          string `json:"key"`
		ProxyBaseURL string `json:"proxyBaseUrl"`
		Model        string `json:"model"`
		ExpiresIn    int    `json:"expiresIn"`
	} `json:"data"`
}

// FetchLLMKey 向 core 换取服务级长效 LLM proxy key（executor 启动时调一次，
// hermes 常驻缓存用）。providerID/model 决定 key 解析到哪个 provider。
func (c *Client) FetchLLMKey(ctx context.Context, providerID uint, model string) (*LLMKeyResponse, error) {
	body, _ := json.Marshal(map[string]any{"providerId": providerID, "model": model})
	url := c.coreURL + "/api/v1/addons/s2s/executor/llm-key"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Executor-Token", c.executorToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm-key request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm-key http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out LLMKeyResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("llm-key unmarshal: %w", err)
	}
	if out.Code != 0 || out.Data.Key == "" {
		return nil, fmt.Errorf("llm-key rejected: code=%d msg=%s", out.Code, out.Message)
	}
	return &out, nil
}
