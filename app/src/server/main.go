package main

import (
	"nucleagent_executor/core"
	"nucleagent_executor/global"
	"nucleagent_executor/initialize"

	"go.uber.org/zap"

	_ "whitestone.top/prism-fusion/addons"
	_ "nucleagent_executor/addons"
)

func main() {
	initializeSystem()
	core.RunServer()
}

func initializeSystem() {
	global.PRISM_VP = core.Viper()
	global.PRISM_LOG = core.Zap()
	zap.ReplaceGlobals(global.PRISM_LOG)
	global.PRISM_DB = initialize.Gorm()
	if global.PRISM_DB != nil {
		global.PRISM_LOG.Info("Database connected successfully")
		initialize.InitTables()
	}
	global.PRISM_LOG.Info("nucleagent-executor initialized")
}
