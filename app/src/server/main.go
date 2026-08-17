package main

import (
	"context"
	"fmt"
	"net/url"
	stdruntime "runtime"
	"os/signal"
	"syscall"
	"time"

	"github.com/nucleagent/nucleagent-shared/a2a"
	"go.uber.org/zap"

	"nucleagent-executor/addons/backend"
	"nucleagent-executor/addons/backend/hermes"
	"nucleagent-executor/addons/session"
	"nucleagent-executor/internal/config"
	"nucleagent-executor/internal/engineclient"
	"nucleagent-executor/internal/runtime"
	"nucleagent-executor/internal/wsclient"

	"github.com/kwhitestone/prism-fusion/core"
	"github.com/kwhitestone/prism-fusion/global"

	// executor 不导入框架内置 auth/rbac addons（不暴露 auth 路由、不连数据库）；
	// 仅注册自身 backend/session addons，供 core 经 WebSocket 接入。
	_ "nucleagent-executor/addons"
)

func main() {
	// 1. 初始化 prism-fusion（配置 + 日志 + HTTP 服务器，但 executor 不连 DB）。
	initializeSystem()

	// 2. 加载 executor 业务配置。
	cfg, err := config.Load()
	if err != nil {
		global.PRISM_LOG.Fatal("config load failed", zap.Error(err))
	}

	// 3. 用配置初始化 session store（覆盖骨架的空文件路径）。
	session.Default = session.NewStore(cfg.SessionFile)

	// 4. 主上下文（响应 SIGINT/SIGTERM）。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 5. 启动 prism-fusion HTTP 服务器（健康检查 + OpenAPI 文档）于后台 goroutine。
	go func() {
		core.RunServer()
	}()

	// 6. 启动 executor 运行时（注册 -> WS -> 派发），阻塞主线程。
	runExecutor(ctx, cfg)

	global.PRISM_LOG.Info("nucleagent-executor stopped")
}

// runExecutor 注册到 core、建立 WS、运行任务派发循环。ctx 取消时优雅退出。
func runExecutor(ctx context.Context, cfg *config.Config) {
	// 收集已注册 backend 的 capability + descriptor。
	caps := backend.Default.Names()
	executors := make([]a2a.DesktopExecutor, 0, len(caps))
	for _, name := range caps {
		b, err := backend.Default.Get(name)
		if err != nil {
			continue
		}
		if d, ok := b.(interface{ Descriptor() a2a.DesktopExecutor }); ok {
			executors = append(executors, d.Descriptor())
		} else {
			executors = append(executors, a2a.DesktopExecutor{ID: name, Type: name, DisplayName: name, Streaming: true})
		}
	}

	// runner + runtime。
	runner := backend.NewRunner(backend.Default)
	// runtime 需要 wsclient 作为 sender，但 wsclient 又需要 runtime 作为 handler。
	// 用一个 sender 桥接：先建 runtime（sender 占位），再建 wsclient，最后回填 sender。
	rt := runtime.New(runner, session.Default, nilSender{}, cfg.MaxSessions)

	// handshake payload。
	handshake := a2a.HandshakePayload{
		DeviceID:     cfg.DeviceID,
		InstanceID:   cfg.InstanceID,
		DeviceName:   cfg.DeviceName,
		BackendType:  cfg.Backend,
		Capabilities: toDesktopCapabilities(caps),
		Executors:    executors,
		Capacity:     &a2a.ExecutorCapacity{MaxConcurrency: cfg.MaxSessions},
		OS:           stdruntime.GOOS,
	}

	// 注册到 core（重试直到成功或 ctx 取消）。
	ec := engineclient.NewClient(cfg.CoreURL, cfg.ExecutorToken, cfg.DeviceID, cfg.InstanceID, cfg.DeviceName)
	wsURL, err := ec.Register(ctx, caps, parseDuration(cfg.RegisterInterval, 5*time.Second))
	if err != nil {
		global.PRISM_LOG.Error("executor register failed, exiting", zap.Error(err))
		return
	}
	// core 下发的 wsUrl 基于其自身配置（常为 localhost），在容器/跨网场景不可达。
	// 注册既然成功，说明 cfg.CoreURL 可达——WS 走同一 host 必然可达，故用它重写 host。
	wsURL = rewriteWSHost(wsURL, cfg.CoreURL)
	global.PRISM_LOG.Info("registered to core", zap.String("wsUrl", wsURL), zap.String("coreUrl", cfg.CoreURL))

	// Hermes 后端配置：
	// 1. 启动 sidecar（hermes→core LLM Proxy 的本地反代，按 Run 注入 key）
	// 2. managed config 写 sidecar 地址 + 固定 token，hermes 常驻复用
	// 3. FetchKey 每次 Run 调，取对话级 TempLLMKey 设进 sidecar
	providerID := cfg.HermesProviderID
	modelName := cfg.HermesModel
	sc, err := hermes.NewSidecar(cfg.CoreURL)
	if err != nil {
		global.PRISM_LOG.Error("start hermes sidecar failed", zap.Error(err))
	}
	// 先 Configure（注入 conf.Workdir），再写 managed config（它读 conf.Workdir）。
	hermes.Configure(hermes.Config{
		Bin:     cfg.HermesBin,
		Workdir: cfg.HermesWorkdir,
		Host:    cfg.HermesHost,
		CoreURL: cfg.CoreURL,
		Model:   modelName,
		Sidecar: sc,
		FetchKey: func() (string, error) {
			if providerID == 0 || modelName == "" {
				return "", fmt.Errorf("hermes provider-id/model not configured")
			}
			k, err := ec.FetchLLMKey(context.Background(), providerID, modelName)
			if err != nil {
				return "", err
			}
			return k.Data.Key, nil
		},
	})
	// managed config 写 sidecar 地址 + 固定 token（hermes 常驻读一次，永不轮换）。
	if sc != nil {
		if err := hermes.WriteManagedConfig(modelName, "sidecar-fixed-token", sc.BaseURL()); err != nil {
			global.PRISM_LOG.Warn("write hermes managed config failed", zap.Error(err))
		}
	}

	// 建 WS 客户端，handler = runtime。
	ws := wsclient.NewClient(wsURL, cfg.ExecutorToken, handshake, rt)
	rt.SetSender(ws) // 回填真实 sender

	// 运行 WS（含自动重连），直到 ctx 取消。
	reconnect := parseDuration(cfg.RegisterInterval, 5*time.Second)
	if err := ws.Run(ctx, reconnect); err != nil {
		global.PRISM_LOG.Warn("ws client stopped", zap.Error(err))
	}
}

// toDesktopCapabilities 把 capability 名转为 DesktopCapability 列表。
func toDesktopCapabilities(names []string) []a2a.DesktopCapability {
	out := make([]a2a.DesktopCapability, 0, len(names))
	for _, n := range names {
		out = append(out, a2a.DesktopCapability{Name: n, DisplayName: n, Streaming: true})
	}
	return out
}

// parseDuration 解析配置里的时间字符串（"5s"/"10s"），失败用 fallback。
func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// nilSender 占位 sender，runtime 在 SetSender 前使用。
type nilSender struct{}

func (nilSender) Send(string, any) error                              { return nil }
func (nilSender) SendWithRequest(string, string, any) error           { return nil }

// rewriteWSHost 把 core 下发的 wsURL 的 host:port 换成 coreURL 的 host:port。
//
// core 的 buildWSURLFromConfig 返回的 wsUrl 基于其自身配置（常 localhost），
// 在 executor 跨网/容器场景不可达。注册成功意味着 coreURL 可达，WS 走同一
// host:port 必然可达，故只换 host、保留 path/query。
// 例：wsURL=ws://localhost:26680/api/...，coreURL=http://host.docker.internal:26680
//     → ws://host.docker.internal:26680/api/...
func rewriteWSHost(wsURL, coreURL string) string {
	if wsURL == "" || coreURL == "" {
		return wsURL
	}
	wu, err := url.Parse(wsURL)
	if err != nil || wu.Host == "" {
		return wsURL
	}
	cu, err := url.Parse(coreURL)
	if err != nil || cu.Host == "" {
		return wsURL
	}
	// 同 host 无需改（裸机 dev 场景，core 与 executor 同机）。
	if wu.Host == cu.Host {
		return wsURL
	}
	wu.Host = cu.Host
	wu.Scheme = "ws"
	if cu.Scheme == "https" {
		wu.Scheme = "wss"
	}
	return wu.String()
}

func initializeSystem() {
	global.PRISM_VP = core.Viper()
	global.PRISM_LOG = core.Zap()
	zap.ReplaceGlobals(global.PRISM_LOG)
	// Executor 不连数据库、不做 JWT 认证：仅启动 HTTP 服务供健康检查 + core WebSocket 接入。
	global.PRISM_LOG.Info("nucleagent-executor initialized")
}
