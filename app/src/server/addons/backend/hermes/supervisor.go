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

// supervisor 单例守护者。hermes 进程常驻复用（不重启），崩溃时指数退避重启。
// key 轮换由 sidecar 处理，hermes 只看到 sidecar 的固定地址。
type supervisor struct {
	mu         sync.Mutex
	proc       *HermesProcess
	started    bool
	lifeCtx    context.Context
	lifeCancel context.CancelFunc
}

var sup = &supervisor{}

// ensureStarted 幂等地启动 hermes 常驻进程（已启动则直接返回）。
func (s *supervisor) ensureStarted() (*HermesProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && s.proc != nil {
		return s.proc, nil
	}
	s.lifeCtx, s.lifeCancel = context.WithCancel(context.Background())
	proc, err := startAndSupervise(s.lifeCtx)
	s.proc = proc
	s.started = true
	if err != nil {
		global.PRISM_LOG.Error("hermes supervisor: failed to start hermes serve", zap.Error(err))
	}
	return proc, err
}

// startAndSupervise 启动 hermes serve + 崩溃重启循环。
func startAndSupervise(ctx context.Context) (*HermesProcess, error) {
	if err := os.MkdirAll(conf.Workdir, 0o755); err != nil {
		return nil, fmt.Errorf("hermes supervisor: mkdir workdir: %w", err)
	}
	proc, err := launchAndWait(ctx)
	if err != nil {
		return nil, err
	}
	if err := waitHealthy(ctx, proc.BaseURL(), healthDeadline); err != nil {
		global.PRISM_LOG.Warn("hermes supervisor: health check failed (continuing)", zap.Error(err))
	}
	go func() {
		backoff := time.Second
		cur := proc
		for {
			if cur.cmd == nil {
				return
			}
			err := cur.cmd.Wait()
			select {
			case <-ctx.Done():
				return
			default:
			}
			global.PRISM_LOG.Warn("hermes serve exited, restarting", zap.Error(err), zap.Duration("backoff", backoff))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			np, lerr := launchAndWait(ctx)
			if lerr != nil {
				global.PRISM_LOG.Error("hermes supervisor: restart failed", zap.Error(lerr))
				continue
			}
			sup.mu.Lock()
			sup.proc = np
			sup.mu.Unlock()
			cur = np
			backoff = time.Second
		}
	}()
	return proc, nil
}

// stop 在 executor 退出时终止 hermes 子进程。
func (s *supervisor) stop() {
	s.mu.Lock()
	cancel := s.lifeCancel
	proc := s.proc
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if proc != nil && proc.cmd != nil && proc.cmd.Process != nil {
		_ = proc.cmd.Process.Kill()
	}
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
