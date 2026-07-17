package addons

// 导入此包会触发所有业务插件 init()。
import (
	_ "nucleagent-executor/addons/backend"
	_ "nucleagent-executor/addons/backend/opencode"
	_ "nucleagent-executor/addons/session"
)
