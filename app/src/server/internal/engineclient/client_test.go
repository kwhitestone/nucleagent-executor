package engineclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRegisterSuccess 验证成功注册返回 wsUrl。
func TestRegisterSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/addons/s2s/executor/register" {
			t.Errorf("path = %q, want register", r.URL.Path)
		}
		if r.Header.Get("X-Executor-Token") != "tok" {
			t.Errorf("missing X-Executor-Token header")
		}
		resp := RegisterResponse{}
		resp.Code = 0
		resp.Data.WSURL = "ws://core/ws"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "dev-1", "inst-1", "test")
	wsURL, err := c.Register(context.Background(), []string{"mock-llm"}, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if wsURL != "ws://core/ws" {
		t.Errorf("wsURL = %q, want ws://core/ws", wsURL)
	}
}

// TestRegisterRetryThenSuccess 验证首次失败后重试成功。
func TestRegisterRetryThenSuccess(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		resp := RegisterResponse{Code: 0}
		resp.Data.WSURL = "ws://ok/ws"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "dev-1", "inst-1", "test")
	wsURL, err := c.Register(context.Background(), []string{"mock-llm"}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if wsURL != "ws://ok/ws" {
		t.Errorf("wsURL = %q", wsURL)
	}
	if calls < 3 {
		t.Errorf("expected >=3 calls, got %d", calls)
	}
}

// TestRegisterMissingToken 验证缺 token 直接报错。
func TestRegisterMissingToken(t *testing.T) {
	c := NewClient("http://localhost", "", "dev", "inst", "name")
	_, err := c.Register(context.Background(), nil, 10*time.Millisecond)
	if err == nil {
		t.Error("expected error for missing token")
	}
}

// TestRegisterContextCancelled 验证 ctx 取消时退出重试。
func TestRegisterContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := NewClient(srv.URL, "tok", "dev", "inst", "name")
	_, err := c.Register(ctx, []string{"x"}, 100*time.Millisecond)
	if err == nil {
		t.Error("expected error on ctx cancel")
	}
}

// TestRegisterRejectedCode 验证 code!=0 视为拒绝并重试。
func TestRegisterRejectedCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返回 code=1（拒绝），但 HTTP 200。
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "bad token"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	c := NewClient(srv.URL, "tok", "dev", "inst", "name")
	_, err := c.Register(ctx, []string{"x"}, 10*time.Millisecond)
	if err == nil {
		t.Error("expected error for rejected code")
	}
}

// TestTruncate 验证截断辅助函数。
func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate(short) = %q", got)
	}
	long := fmt.Sprintf("%0200d", 0)
	got := truncate(long, 50)
	if len(got) > 54 { // 50 + "..."
		t.Errorf("truncate result too long: %d", len(got))
	}
}
