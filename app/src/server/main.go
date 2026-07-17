package main

import (
	"whitestone.top/prism-fusion/core"
	"whitestone.top/prism-fusion/global"
	"whitestone.top/prism-fusion/initialize"

	"go.uber.org/zap"

	_ "whitestone.top/prism-fusion/addons"
	_ "nucleagent-executor/addons"
)

func main() {
	initializeSystem()
	core.RunServer()
}

func initializeSystem() {
	global.PRISM_VP = core.Viper()
	global.PRISM_LOG = core.Zap()
	zap.ReplaceGlobals(global.PRISM_LOG)
	// Executor 默认不连数据库：未配置 DB 时 Gorm() 返回 nil，跳过 InitTables。
	global.PRISM_DB = initialize.Gorm()
	if global.PRISM_DB != nil {
		global.PRISM_LOG.Info("Database connected successfully")
		initialize.InitTables()
	}
	global.PRISM_LOG.Info("nucleagent-executor initialized")
}
