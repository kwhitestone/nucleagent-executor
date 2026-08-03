// Package config executor 配置加载。
//
// executor 复用 prism-fusion 的 core.Viper() 读取 config.yaml，
// 但 nucleagent 业务段不在框架 config.Server 结构里，故此处用
// global.PRISM_VP 单独读取 nucleagent.* 键。
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"whitestone.top/prism-fusion/global"
)

// Config executor 业务配置（对应 config.yaml 的 nucleagent 段）。
type Config struct {
	CoreURL        string // core API 地址（http://localhost:26680）
	DataDir        string // 数据目录（日志/session 等）
	MaxSessions    int    // 最大并发 session
	ExecutorToken  string // S2S 校验 X-Executor-Token（与 core 共享）
	Backend        string // 默认执行后端（opencode / mock-llm / ...）
	SessionFile    string // TaskSession JSON 持久化文件
	DeviceID       string // 逻辑设备 ID（灰度池分组，默认 nucleagent-executor）
	InstanceID     string // 实例 ID（空则写文件稳定复用）
	DeviceName     string // 展示名
	RegisterInterval string // 注册重试间隔（默认 5s）
	HeartbeatInterval string // 心跳间隔（默认 10s）
}

// Load 从 global.PRISM_VP 读取 nucleagent 段并展开环境变量。
// 必须在 core.Viper() 之后调用。
func Load() (*Config, error) {
	vp := global.PRISM_VP
	if vp == nil {
		return nil, fmt.Errorf("config: global.PRISM_VP not initialized")
	}

	cfg := &Config{
		CoreURL:           vp.GetString("nucleagent.core-url"),
		DataDir:           vp.GetString("nucleagent.data-dir"),
		MaxSessions:       vp.GetInt("nucleagent.max-sessions"),
		ExecutorToken:     vp.GetString("nucleagent.executor-token"),
		Backend:           vp.GetString("nucleagent.backend"),
		SessionFile:       vp.GetString("nucleagent.session.file"),
		DeviceID:          vp.GetString("nucleagent.device-id"),
		InstanceID:        vp.GetString("nucleagent.instance-id"),
		DeviceName:        vp.GetString("nucleagent.device-name"),
		RegisterInterval:  vp.GetString("nucleagent.register-interval"),
		HeartbeatInterval: vp.GetString("nucleagent.heartbeat-interval"),
	}

	// 展开环境变量（vp.AutomaticEnv 已开，但显式默认值更稳）。
	cfg.CoreURL = envDefault("CORE_URL", cfg.CoreURL, "http://localhost:26680")
	cfg.DataDir = envDefault("DATA_DIR", cfg.DataDir, "./data")
	cfg.SessionFile = envDefault("SESSION_FILE", cfg.SessionFile, filepath.Join(cfg.DataDir, "task_sessions.json"))
	cfg.ExecutorToken = os.Getenv("EXECUTOR_TOKEN")
	if cfg.DeviceID == "" {
		cfg.DeviceID = envDefault("EXECUTOR_DEVICE_ID", "", "nucleagent-executor")
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = os.Getenv("EXECUTOR_INSTANCE_ID")
	}
	if cfg.Backend == "" {
		cfg.Backend = "mock-llm" // MVP 默认走 mock，OpenCode 桥接后置
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 10
	}
	if cfg.RegisterInterval == "" {
		cfg.RegisterInterval = "5s"
	}
	if cfg.HeartbeatInterval == "" {
		cfg.HeartbeatInterval = "10s"
	}

	// 确保 data 目录存在。
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return nil, fmt.Errorf("config: mkdir data-dir: %w", err)
		}
	}

	// instance_id 留空时写文件稳定复用（容器重启后复用同一 ID）。
	if cfg.InstanceID == "" {
		instFile := filepath.Join(cfg.DataDir, "instance_id")
		if b, err := os.ReadFile(instFile); err == nil && len(b) > 0 {
			cfg.InstanceID = string(b)
		} else {
			cfg.InstanceID = uuid.NewString()
			_ = os.WriteFile(instFile, []byte(cfg.InstanceID), 0o644)
		}
	}

	return cfg, nil
}

// envDefault 返回环境变量值，空则返回 fallback。
func envDefault(key, cur, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if cur != "" {
		return cur
	}
	return fallback
}
