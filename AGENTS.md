# nucleagent-executor

执行器：任务执行引擎，承载 OpenCode/Hermes Agent 上下文。基于 Prism Fusion 框架。


## Commit Message Language (IRON RULE)

**All commit messages MUST be written in English.** No exceptions.

- Subjects and bodies: English only. No Chinese characters anywhere in the message.
- Type prefixes follow Conventional Commits (`feat:`, `fix:`, `chore:`, `refactor:`, `docs:`, `style:`, `perf:`, `test:`).
- Referencing code identifiers, paths, or domain terms is fine; prose must be English.

Rationale: these repositories are open-sourced on GitHub; Chinese commit messages
make history unreadable to international contributors and pollute git log tooling.

## 构建

> 前置：首次构建需先在 repo 根目录执行 `git submodule update --init` 拉取 prism-fusion，再 `go work sync`。

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
- S2S 认证：注册/心跳/WebSocket 连接均携带 `X-Executor-Token`（由 core 签发）
- Session 持久化用 JSON 文件（`task_sessions.json`），不用数据库
- LLM 调用通过 core 的 LLM Proxy 临时 Key，不直接持有 API key
- CORS：`cors.mode: strict-whitelist`，通过环境变量配置允许的前端来源（WEB_FRONTEND_URL + EXECUTOR_FRONTEND_URL），支持分布式部署

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

## 配置 (config.yaml)

框架基础配置（system/jwt/auth/zap/db 等）见 prism-fusion；nucleagent 执行器专属配置在 `nucleagent` 段：

```yaml
nucleagent:
  core-url: 'http://localhost:26680'       # core 后端地址（注册/心跳用）
  executor-token: '${EXECUTOR_TOKEN}'     # S2S 认证 token（X-Executor-Token），注册时校验
  backend: 'opencode'                      # 默认执行后端（opencode / hermes）
  sandbox:
    root: './sandboxes'                    # 沙箱工作目录根（按 session 隔离）
  session:
    file: './task_sessions.json'           # TaskSession JSON 持久化文件
  max-sessions: 10                         # 最大并发会话数
```

WebSocket 地址（`wsUrl`）由注册接口 `POST /api/v1/addons/s2s/executor/register` 返回，不在 config 写死。

## 边界

- **Always**: 新 Backend 必须实现 Backend 接口
- **Always**: 流式输出用 StreamReporter，不直接写 WebSocket
- **Never**: 禁止连数据库
- **Never**: 禁止直接持有 LLM API key
- **Never**: 禁止 import nucleagent-core
