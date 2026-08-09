// sidecar.go 实现 hermes 与 core LLM Proxy 之间的轻量 HTTP 反向代理。
//
// 架构：
//   hermes-agent (常驻, api_key=固定值, base_url=http://127.0.0.1:<sidecar>/v1)
//       │  固定凭证，hermes 永不感知 key 轮换
//       ▼
//   sidecar (本文件, Go 进程内 HTTP 反代)
//       │  按当前活跃 Run 注入对应的 TempLLMKey（Authorization: Bearer）
//       ▼
//   core /api/llm-proxy/v1/chat/completions
//
// Run 开始时调 SetActive(key) 设当前 key；Run 结束调 ClearActive。
// sidecar 用该 key 转发，core 侧 RefreshTTL 保证 key 不过期。
package hermes

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"go.uber.org/zap"

	"whitestone.top/prism-fusion/global"
)

// Sidecar hermes→core LLM Proxy 的本地反向代理。
//
// 监听本地随机端口，hermes 的 managed config base_url 指向它。
// 转发时注入当前活跃 Run 的 TempLLMKey（core 侧 RefreshTTL 续期）。
type Sidecar struct {
	server  *http.Server
	addr    string // 监听地址 127.0.0.1:<port>
	coreURL string // core 根地址

	mu     sync.RWMutex
	active string // 当前活跃的 TempLLMKey（llmk_ 前缀）
}

// NewSidecar 创建并启动 sidecar，监听本地随机端口。
func NewSidecar(coreURL string) (*Sidecar, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sidecar: listen: %w", err)
	}
	addr := ln.Addr().String()
	sc := &Sidecar{addr: addr, coreURL: strings.TrimRight(coreURL, "/")}

	target, _ := url.Parse(sc.coreURL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{Proxy: nil}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// path 重写：sidecar 收到 /v1/chat/completions → core /api/llm-proxy/v1/chat/completions
		stripped := strings.TrimPrefix(req.URL.Path, "/v1")
		req.URL.Path = "/api/llm-proxy/v1" + stripped
		if stripped == "" || stripped == "/" {
			req.URL.Path = "/api/llm-proxy/v1/chat/completions"
		}
		req.URL.RawPath = ""
		originalDirector(req)
		req.Host = target.Host
		// 注入当前活跃 key（替换 hermes 带的固定 token）。
		sc.mu.RLock()
		key := sc.active
		sc.mu.RUnlock()
		req.Header.Del("Authorization")
		req.Header.Del("x-llm-proxy-key")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	sc.server = &http.Server{Handler: proxy}
	go func() { _ = sc.server.Serve(ln) }()
	global.PRISM_LOG.Info("hermes sidecar started", zap.String("addr", addr))
	return sc, nil
}

// BaseURL 返回 hermes managed config 应写的 base_url（http://127.0.0.1:<port>/v1）。
func (s *Sidecar) BaseURL() string { return "http://" + s.addr + "/v1" }

// SetActive 设置当前活跃 Run 的 TempLLMKey。
func (s *Sidecar) SetActive(key string) {
	s.mu.Lock()
	s.active = key
	s.mu.Unlock()
}

// ClearActive 清除活跃 key（Run 结束时调）。
func (s *Sidecar) ClearActive() {
	s.mu.Lock()
	s.active = ""
	s.mu.Unlock()
}

// Shutdown 优雅关闭。
func (s *Sidecar) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
