// Package wsclient executor 端 WebSocket 客户端。
//
// 连接 core 下发的 wsUrl，发送 handshake，接收 envelope（含分块重组），
// 把完整信封交给 handler 处理。支持自动重连。
package wsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nucleagent/nucleagent-shared/a2a"
)

// Handler 处理收到的完整信封。
type Handler interface {
	HandleEnvelope(ctx context.Context, env *a2a.Envelope) error
}

// HandlerFunc 函数适配器。
type HandlerFunc func(ctx context.Context, env *a2a.Envelope) error

func (f HandlerFunc) HandleEnvelope(ctx context.Context, env *a2a.Envelope) error { return f(ctx, env) }

// Client WebSocket 客户端。
type Client struct {
	wsURL       string
	token       string // X-Executor-Token
	handshake   a2a.HandshakePayload
	handler     Handler

	connMu sync.Mutex
	conn   *websocket.Conn

	writeMu sync.Mutex // 串行化 WS 写入

	// 分块重组缓冲：chunkID -> 待组合的片段。
	chunkMu sync.Mutex
	chunks  map[string][]*a2a.Envelope
}

// NewClient 构造客户端。handshake 是握手时上报的身份/能力。
func NewClient(wsURL, token string, handshake a2a.HandshakePayload, handler Handler) *Client {
	return &Client{
		wsURL:    wsURL,
		token:    token,
		handshake: handshake,
		handler:  handler,
		chunks:   make(map[string][]*a2a.Envelope),
	}
}

// Run 阻塞运行：连接 -> 握手 -> 读循环。连接断开时按 reconnect 间隔重连，
// 直到 ctx 取消。
func (c *Client) Run(ctx context.Context, reconnect time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := c.runOnce(ctx); err != nil {
			// ctx 取消则直接返回，否则等待重连。
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(os.Stderr, "wsclient: runOnce ended, reconnect in %v: %v\n", reconnect, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reconnect):
			}
		}
	}
}

// runOnce 执行一次完整的连接生命周期。
func (c *Client) runOnce(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		// 显式不走系统代理：executor → core 的 WebSocket 是 localhost 互调，
		// 宿主机的 HTTP_PROXY 会导致连不上（与 engineclient 同理）。
		Proxy: nil,
	}
	hdr := map[string][]string{}
	if c.token != "" {
		hdr["X-Executor-Token"] = []string{c.token}
	}

	conn, _, err := dialer.DialContext(ctx, c.wsURL, hdr)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	// 重置分块缓冲（新连接，旧分片失效）。
	c.chunkMu.Lock()
	c.chunks = make(map[string][]*a2a.Envelope)
	c.chunkMu.Unlock()

	// 1. 发送握手。
	if err := c.sendHandshake(ctx); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	// 2. 启动 ping ticker。
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go c.pingLoop(pingCtx, 30*time.Second)

	// 3. 读循环。
	return c.readLoop(ctx, conn)
}

// sendHandshake 发送握手信封。
func (c *Client) sendHandshake(ctx context.Context) error {
	env, err := a2a.NewEnvelope(nowMillis(), a2a.EnvHandshake, c.handshake)
	if err != nil {
		return err
	}
	return c.sendEnvelope(env)
}

// readLoop 持续读取并处理信封。
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}
		var env a2a.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			// 无法解析的帧跳过，不致命。
			continue
		}
		// 分块消息需重组；非分块直接处理。
		if env.ChunkID != "" {
			c.handleChunk(&env)
			continue
		}
		if err := c.dispatch(ctx, &env); err != nil {
			// handler 错误不中断读循环，由 handler 自行决定如何上报。
			_ = err
		}
	}
}

// handleChunk 收集分片，收齐后重组并 dispatch。
func (c *Client) handleChunk(env *a2a.Envelope) {
	c.chunkMu.Lock()
	c.chunks[env.ChunkID] = append(c.chunks[env.ChunkID], env)

	// 尚未收齐。
	if len(c.chunks[env.ChunkID]) < env.ChunkTotal {
		c.chunkMu.Unlock()
		return
	}
	parts := c.chunks[env.ChunkID]
	delete(c.chunks, env.ChunkID)
	c.chunkMu.Unlock()

	complete, _, err := a2a.DecodeEnvelopeFrames(parts)
	if err != nil || len(complete) == 0 {
		return
	}
	_ = c.dispatch(context.Background(), complete[0])
}

// dispatch 把完整信封交给 handler。
func (c *Client) dispatch(ctx context.Context, env *a2a.Envelope) error {
	return c.handler.HandleEnvelope(ctx, env)
}

// Send 发送任意 payload 的信封（自动分块）。
func (c *Client) Send(kind string, payload any) error {
	env, err := a2a.NewEnvelope(nowMillis(), kind, payload)
	if err != nil {
		return err
	}
	return c.sendEnvelope(env)
}

// SendWithRequest 发送带 RequestID 的信封。
func (c *Client) SendWithRequest(kind, requestID string, payload any) error {
	env, err := a2a.NewEnvelopeWithRequest(nowMillis(), kind, requestID, payload)
	if err != nil {
		return err
	}
	return c.sendEnvelope(env)
}

// sendEnvelope 编码（含分块）并写入 WS。
func (c *Client) sendEnvelope(env *a2a.Envelope) error {
	frames, err := a2a.EncodeEnvelopeFrames(env)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("ws not connected")
	}
	for _, f := range frames {
		if err := conn.WriteMessage(websocket.TextMessage, f); err != nil {
			return fmt.Errorf("ws write: %w", err)
		}
	}
	return nil
}

// pingLoop 定期发 ping，保活连接。
func (c *Client) pingLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Send(a2a.EnvPing, a2a.PingPayload{SentAt: nowMillis()}); err != nil {
				return
			}
		}
	}
}

// nowMillis 返回当前毫秒时间戳。
func nowMillis() int64 {
	return time.Now().UnixMilli()
}
