package addons

// 导入此包会触发所有业务插件 init()。
import (
	// 可插拔执行后端注册表 + runner
	_ "nucleagent-executor/addons/backend"
	// MockLLM 后端（打通协议用，真实 OpenCode 桥接后置）
	_ "nucleagent-executor/addons/backend/mockllm"
	// OpenCode 后端骨架（占位，真实桥接待接入）
	_ "nucleagent-executor/addons/backend/opencode"
	// TaskSession 管理（内存 + JSON 文件）
	_ "nucleagent-executor/addons/session"
)
