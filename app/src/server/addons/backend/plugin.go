package backend

import "github.com/kwhitestone/prism-fusion/plugin"

// BackendPlugin 执行后端插件（注册表 + runner 的宿主）。
// Executor 不对前端暴露 HTTP，故无路由/模型；具体后端在子包自注册到 Default。
type BackendPlugin struct {
	plugin.BasePlugin
}

func init() {
	plugin.Register(&BackendPlugin{
		BasePlugin: plugin.BasePlugin{
			PluginName:        "backend",
			PluginDescription: "执行后端插件 - 可插拔 Backend 注册表 + runner",
		},
	})
}

func (p *BackendPlugin) RoutePrefix() string {
	return "/api/v1/addons/backend"
}
