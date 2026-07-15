module nucleagent-executor

go 1.25

require (
	github.com/nucleagent/nucleagent-shared v0.0.0
	whitestone.top/prism-fusion v0.0.0
	github.com/gin-gonic/gin v1.11.0
	github.com/gorilla/websocket v1.5.3
)

replace (
	github.com/nucleagent/nucleagent-shared => ../../nucleagent-shared
	whitestone.top/prism-fusion => ./prism-fusion/src/server
)
