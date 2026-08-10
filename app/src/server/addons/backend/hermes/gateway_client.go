// Package hermes 把 Hermes Agent（Python）接入为执行后端。
//
// 通信接口是 `hermes serve` 暴露的 WebSocket JSON-RPC gateway（与
// agentia-hermes-shell 的 Rust 客户端同协议，见 shell/src/hermes/client.rs）：
//
//   - 连接 ws://<host>:<port>/api/ws?token=<session-token> 后，服务端先发
//     gateway.ready event（无需回应）。
//   - 请求是标准 JSON-RPC 2.0（带 id），响应回 result 或 error。
//   - 事件是 method=="event" 的通知，真实类型在 params.type。
//
// 本文件实现 WS JSON-RPC 客户端：Dial 建连，Call 发请求等响应，Events
// 暴露事件流。Run 逻辑在 hermes.go，用 Call + Events 驱动一次对话。
package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// defaultCallTimeout 单个 JSON-RPC 请求等待响应的默认上限。
// prompt.submit 在服务端异步执行，通常秒级返回；留足余量。
const defaultCallTimeout = 120 * time.Second

// rpcRequest JSON-RPC 2.0 请求帧。
type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// rpcFrame 服务端下发的任意帧（响应或事件）。
type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.Number     `json:"id,omitempty"` // 数字或字符串，统一成字符串
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError JSON-RPC error 对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// GatewayEvent 解析后的一个 gateway 事件。
//
// EventType 对应 params.type（如 message.delta / thinking.delta /
// message.complete）；Payload 是 params.payload 原文，由上层按类型解读。
type GatewayEvent struct {
	EventType string
	SessionID string
	Payload   json.RawMessage
}

// 事件类型常量（与 shell/src/hermes/client.rs events 模块对齐）。
const (
	evtGatewayReady    = "gateway.ready"
	evtMessageStart    = "message.start"
	evtMessageDelta    = "message.delta"
	evtMessageComplete = "message.complete"
	evtReasoningDelta  = "reasoning.delta"
	evtThinkingDelta   = "thinking.delta"
	evtToolStart       = "tool.start"
	evtToolComplete    = "tool.complete"
	evtError           = "error"
	evtSubagentStart   = "subagent.start"
	evtSubagentText    = "subagent.text"
)

// GatewayClient Hermes WebSocket JSON-RPC 客户端。
//
// 一个客户端对应一条 WS 连接（一次 Run 一条）。读 goroutine 把帧分成
// 响应（按 id 路由到 pending）和事件（送入 eventCh）。
type GatewayClient struct {
	conn *websocket.Conn

	writeMu sync.Mutex // 串行化写帧（gorilla Conn 不支持并发写）

	nextID    int64
	idMu      sync.Mutex
	pending   map[string]chan rpcFrame // id(字符串) -> response 等待者
	pendingMu sync.Mutex

	eventCh chan GatewayEvent // 事件流（带缓冲，避免读循环阻塞）

	closeOnce sync.Once
	done      chan struct{} // 读循环退出信号
}

// Dial 连接 Hermes gateway WS。readyTimeout 内等不到连接即失败。
func Dial(ctx context.Context, wsURL string) (*GatewayClient, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hermes gateway dial: %w", err)
	}

	c := &GatewayClient{
		conn:    conn,
		pending: make(map[string]chan rpcFrame),
		eventCh: make(chan GatewayEvent, 64),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Close 关闭连接与内部 goroutine。可重复调用。
func (c *GatewayClient) Close() {
	c.closeOnce.Do(func() {
		_ = c.conn.Close()
		// readLoop 感知 conn 关闭后退出，关闭 done 让 Call 不再死等。
	})
	// 失败所有在途请求。
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		select {
		case ch <- rpcFrame{Error: &rpcError{Message: "connection closed"}}:
		default:
		}
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
}

// Call 发送 JSON-RPC 请求并等待对应 id 的响应。ctx 取消时放弃等待。
func (c *GatewayClient) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.idMu.Lock()
	c.nextID++
	id := c.nextID
	c.idMu.Unlock()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	respCh := make(chan rpcFrame, 1)
	idKey := fmt.Sprintf("%d", id)
	c.pendingMu.Lock()
	c.pending[idKey] = respCh
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, idKey)
		c.pendingMu.Unlock()
	}()

	c.writeMu.Lock()
	err = c.conn.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("hermes gateway send %s: %w", method, err)
	}

	timeout := defaultCallTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 && d < timeout {
			timeout = d
		}
	}
	select {
	case frame := <-respCh:
		if frame.Error != nil {
			return nil, fmt.Errorf("hermes gateway %s: [%d] %s", method, frame.Error.Code, frame.Error.Message)
		}
		return frame.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("hermes gateway %s: timed out waiting for response", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("hermes gateway %s: connection closed", method)
	}
}

// Send 发送 JSON-RPC 请求但不等待响应（fire-and-forget）。供 prompt.submit 用：
// hermes 的 prompt.submit ack 可能和首批事件同时到达，阻塞等 ack 会延迟事件处理。
func (c *GatewayClient) Send(method string, params interface{}) error {
	c.idMu.Lock()
	c.nextID++
	id := c.nextID
	c.idMu.Unlock()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	err = c.conn.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()
	return err
}

// Events 返回事件流通道。读循环把所有 method=="event" 的通知送入此处。
func (c *GatewayClient) Events() <-chan GatewayEvent { return c.eventCh }

// Done 在读循环退出（连接断开）时关闭。
func (c *GatewayClient) Done() <-chan struct{} { return c.done }

// readLoop 持续读帧并分发：响应按 id 路由到 pending，事件送入 eventCh。
// 任何读错误都结束循环并关闭 done。
func (c *GatewayClient) readLoop() {
	defer func() {
		close(c.done)
		close(c.eventCh)
		c.pendingMu.Lock()
		for id, ch := range c.pending {
			select {
			case ch <- rpcFrame{Error: &rpcError{Message: "connection closed"}}:
			default:
			}
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
	}()

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame rpcFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			// 非 JSON 帧（如 hermes 偶发的 stdout 调试行）忽略，不断连。
			continue
		}

		// 事件通知：method=="event"，真实类型在 params.type。
		if frame.Method == "event" {
			evt := parseEvent(frame.Params)
			if evt.EventType != "" {
				select {
				case c.eventCh <- evt:
				default:
					// eventCh 满了（消费者慢）：丢弃老事件保活——不应在正常
					// 流式场景发生（64 缓冲足够）。
				}
			}
			continue
		}

		// 响应：按 id 路由。id 可能是数字或字符串，统一字符串化。
		if frame.ID != "" {
			c.pendingMu.Lock()
			ch, ok := c.pending[string(frame.ID)]
			c.pendingMu.Unlock()
			if ok {
				select {
				case ch <- frame:
				default:
				}
			}
		}
	}
}

// parseEvent 从 session/update 风格的 params 解析出 GatewayEvent。
// params 结构：{type, session_id, payload}。
func parseEvent(params json.RawMessage) GatewayEvent {
	var raw struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		Payload   json.RawMessage `json:"payload"`
	}
	if len(params) == 0 {
		return GatewayEvent{}
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return GatewayEvent{}
	}
	return GatewayEvent{EventType: raw.Type, SessionID: raw.SessionID, Payload: raw.Payload}
}

// extractText 从事件 payload 里取 text 字段（message.delta / thinking.delta 用）。
func extractText(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.Text
}
