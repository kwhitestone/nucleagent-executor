// supervisor.go 拉起并守护 `hermes serve` 子进程。
//
// executor 启动后 HermesBackend 首次 Run 时懒启动一个 hermes 进程，之后
// 所有任务复用这个常驻进程（通过 WS 多路复用）。进程崩溃时指数退避重启。
// 参考 agentia-app/agentia-executor/supervisor.go（Go，指数退避）与
// agentia-executor-hermes/shell/src/hermes/process.rs（Rust，端口 sentinel）。
package hermes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"whitestone.top/prism-fusion/global"
)

// readySentinel hermes serve 启动完成后在 stdout 打印的端口通告行前缀。
// 见 process.rs:8 / web_server.py:17236。
const readySentinel = "HERMES_BACKEND_READY"

// readyTimeout 等待 sentinel 的上限。hermes 首启含 Python 解释器初始化 +
// provider 解析，给足余量。
const readyTimeout = 180 * time.Second

// healthDeadline 轮询 /api/health 的总时限。
const healthDeadline = 60 * time.Second

// maxBackoff 崩溃重启退避上限。
const maxBackoff = 15 * time.Second

// HermesProcess 一个运行中的 hermes serve 子进程。
type HermesProcess struct {
	cmd          *exec.Cmd
	port         int
	host         string
	sessionToken string
}

// BaseURL hermes HTTP 根地址（健康检查用）。
func (p *HermesProcess) BaseURL() string {
	return fmt.Sprintf("http://%s:%d", p.host, p.port)
}

// WSURL 带 token 的 WebSocket gateway 地址。
func (p *HermesProcess) WSURL() string {
	return fmt.Sprintf("ws://%s:%d/api/ws?token=%s", p.host, p.port, p.sessionToken)
}

// supervisor 单例守护者。每个 Run 重启一次 hermes 进程——因为 hermes 在 agent
// init 时缓存 provider + api_key（managed config 的 api_key 是每次 Run 轮换的
// LLM proxy 临时 key），不重启会让所有对话复用首个 key。冷启动 ~3s 可接受。
type supervisor struct {
	mu   sync.Mutex
	proc *HermesProcess
}

var sup = &supervisor{}

// startFresh 杀掉旧 hermes 进程，启动一个新的并等就绪。每次 Run 前调用，
// 确保 hermes 重新读取刚写好的 managed config（含本次对话的临时 key）。
func (s *supervisor) startFresh() (*HermesProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 终止上一个 hermes 进程（若有）。
	if s.proc != nil {
		s.killProc(s.proc)
		s.proc = nil
	}

	if err := os.MkdirAll(conf.Workdir, 0o755); err != nil {
		return nil, fmt.Errorf("hermes supervisor: mkdir workdir: %w", err)
	}

	proc, err := launchAndWait(context.Background())
	if err != nil {
		global.PRISM_LOG.Error("hermes supervisor: failed to start hermes serve", zap.Error(err))
		return nil, err
	}
	// 健康检查（sentinel 只证明绑了端口，健康检查证明 ASGI 就绪）。
	if err := waitHealthy(context.Background(), proc.BaseURL(), healthDeadline); err != nil {
		global.PRISM_LOG.Warn("hermes supervisor: health check failed (continuing)", zap.Error(err))
	}
	s.proc = proc
	return proc, nil
}

// stop 在 executor 退出时终止当前 hermes 子进程。
func (s *supervisor) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil {
		s.killProc(s.proc)
		s.proc = nil
	}
}

// killProc 终止一个 hermes 进程并回收。
func (s *supervisor) killProc(p *HermesProcess) {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()
}

// launchAndWait 单次启动 hermes serve 并阻塞到拿到端口 sentinel。
func launchAndWait(ctx context.Context) (*HermesProcess, error) {
	token := uuid.NewString()
	cmd := exec.CommandContext(ctx, conf.Bin, "serve", "--host", conf.Host, "--port", "0")
	cmd.Env = append(os.Environ(),
		"HERMES_HOME="+conf.Workdir,
		"HERMES_MANAGED_DIR="+filepath.Join(conf.Workdir, "managed"),
		"PYTHONUNBUFFERED=1",
		"HERMES_DASHBOARD_SESSION_TOKEN="+token,
	)
	cmd.Cancel = func() error {
		// ctx 取消时发 SIGKILL，避免 Python 子进程残留。
		return cmd.Process.Kill()
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("hermes supervisor: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("hermes supervisor: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("hermes supervisor: start %s: %w", conf.Bin, err)
	}

	// stderr → 日志（debug），避免 pipe buffer 满导致子进程阻塞。
	go drainLog(stderr, "hermes.stderr")
	// stdout → 找 sentinel。
	portCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go scanReady(stdout, portCh, errCh)

	select {
	case port := <-portCh:
		global.PRISM_LOG.Info("hermes serve ready",
			zap.String("bin", conf.Bin), zap.Int("port", port), zap.String("host", conf.Host))
		return &HermesProcess{cmd: cmd, port: port, host: conf.Host, sessionToken: token}, nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("hermes supervisor: %w", err)
	case <-time.After(readyTimeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("hermes supervisor: timed out after %s waiting for %s", readyTimeout, readySentinel)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}
}

// scanReady 逐行读 stdout，命中 "HERMES_BACKEND_READY port=<N>" 即送端口。
func scanReady(r io.Reader, portCh chan<- int, errCh chan<- error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		global.PRISM_LOG.Debug("hermes.stdout", zap.String("line", line))
		if port := parseReadyPort(line); port > 0 {
			portCh <- port
			return
		}
	}
	if err := scanner.Err(); err != nil {
		errCh <- fmt.Errorf("stdout scan: %w", err)
	}
}

// parseReadyPort 从 "HERMES_BACKEND_READY port=<N>" 解析端口。
// 同时兼容 "HERMES_DASHBOARD_READY port=<N>"（见 process.rs:261）。
func parseReadyPort(line string) int {
	line = strings.TrimSpace(line)
	rest := strings.TrimPrefix(line, readySentinel)
	if rest == line {
		rest = strings.TrimPrefix(line, "HERMES_DASHBOARD_READY")
		if rest == line {
			return 0
		}
	}
	for _, tok := range strings.Fields(rest) {
		if strings.HasPrefix(tok, "port=") {
			var port int
			_, err := fmt.Sscanf(tok, "port=%d", &port)
			if err == nil && port > 0 {
				return port
			}
		}
	}
	return 0
}

// waitHealthy 轮询 /api/health。200/401/403 都算就绪（兼容 0.18.x，见 process.rs:279）。
func waitHealthy(ctx context.Context, baseURL string, deadline time.Duration) error {
	client := &http.Client{Timeout: 3 * time.Second}
	url := baseURL + "/api/health"
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("hermes health check failed after %s", deadline)
}

// drainLog 把 reader 按行写到 zap debug 日志。
func drainLog(r io.Reader, target string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		global.PRISM_LOG.Debug(target, zap.String("line", scanner.Text()))
	}
}
