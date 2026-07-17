package session

import "whitestone.top/prism-fusion/plugin"

// Default 默认会话存储。骨架阶段 file 为空（仅内存）；
// 真实路径由 config.nucleagent.session.file 注入（TODO）。
var Default = NewStore("")

// SessionPlugin 会话插件。
type SessionPlugin struct {
	plugin.BasePlugin
}

func init() {
	plugin.Register(&SessionPlugin{
		BasePlugin: plugin.BasePlugin{
			PluginName:        "session",
			PluginDescription: "会话插件 - TaskSession 管理（内存 + JSON 文件）",
		},
	})
}

func (p *SessionPlugin) RoutePrefix() string {
	return "/api/v1/addons/session"
}
