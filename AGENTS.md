# nucleagent-executor

执行器：任务执行引擎，承载 OpenCode/Hermes Agent 上下文。基于 Prism Fusion 框架。

## 构建

```bash
cd app/src/server
go work sync
go build ./...
go run main.go        # 启动（需要 core 后端在运行）
```

## 架构约束

- Backend 接口：`Capability() / Run(ctx, req, reporter) / Kill(ctx, session)`
- StreamReporter 接口：`TextDelta / ThinkingDelta / Progress / ToolUse / Flush`
- 通过 WebSocket 连接 core，不暴露 HTTP API 给前端
- Session 持久化用 JSON 文件（`task_sessions.json`），不用数据库
- LLM 调用通过 core 的 LLM Proxy 临时 Key，不直接持有 API key

## Addons

| addon | 职责 |
|-------|------|
| session | TaskSession 管理（内存 + JSON 文件） |
| backend | 可插拔执行后端（OpenCode / Hermes） |
| sandbox | 沙箱管理（工作目录隔离） |
| bridge | WebSocket 连接 core（接收任务 + 回报结果） |

## 依赖

- `nucleagent-shared` (协议类型) via go.work replace
- `prism-fusion` (框架) via git submodule + go.work
- `nucleagent-core` (WebSocket 连接，不 import)
- 不连数据库

## 边界

- **Always**: 新 Backend 必须实现 Backend 接口
- **Always**: 流式输出用 StreamReporter，不直接写 WebSocket
- **Never**: 禁止连数据库
- **Never**: 禁止直接持有 LLM API key
- **Never**: 禁止 import nucleagent-core
