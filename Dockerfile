# syntax=docker/dockerfile:1
# =============================================================================
# nucleagent-executor 单容器镜像：Go executor + Hermes Agent（Python）运行时。
#
# Go executor 作 PID 1，用 exec.Command 拉起 `hermes serve` 子进程（二者共享
# 同一文件系统，见 addons/backend/hermes/supervisor.go）。
#
# ⚠️ 构建顺序：hermes-agent 的 Dockerfile 要求 build context = hermes-agent/
# 目录（COPY 相对其根）。本 Dockerfile 的 build context 又需要 workspace 根
# （Go replace 指令）。两者 context 不同，故先用 build.sh 构建 hermes-base 镜像，
# 本 Dockerfile 第一阶段 FROM hermes-base。一条命令搞定：
#
#   ./build.sh            # 构建 hermes-base + nucleagent-executor
#
# 或手动两步：
#   docker build -t hermes-base hermes-agent/
#   docker build -t nucleagent-executor -f Dockerfile ..   # context=workspace 根
#
# 三阶段：
#   1. hermes-base —— 由 hermes-agent/Dockerfile 全量构建（venv + web + 依赖）
#   2. go-build    —— 构建 Go executor 二进制（含 replace 指向的本地 module）
#   3. final       —— 基于 hermes-base，叠入 Go 二进制，ENTRYPOINT 改为 executor
# =============================================================================

# ---- Stage 1: Hermes Agent 运行时（由 build.sh 预先构建好的 hermes-base）--
# hermes-base 镜像含完整的 /opt/hermes（venv + web_dist + bin）+ sqlite/node
# 等依赖。直接以其为基础，不重建 Python 运行时。
FROM hermes-base AS hermes

# ---- Stage 2: Go executor 二进制 -----------------------------------------
FROM golang:1.25 AS go-build
ENV CGO_ENABLED=0 GO111MODULE=on GOPROXY=https://goproxy.cn,direct
WORKDIR /build

# Go module 根在 nucleagent-executor/app/src/server，replace 指向上层
# nucleagent-shared / prism-fusion，故把 executor + 两个依赖 module 都复制
# 进来（按 replace 的相对路径摆放）。
COPY nucleagent-executor/app/src/server/ ./nucleagent-executor/app/src/server/
COPY nucleagent-shared/                  ./nucleagent-shared/
COPY prism-fusion/src/server/            ./prism-fusion/src/server/

WORKDIR /build/nucleagent-executor/app/src/server
RUN go build -ldflags="-s -w" -o /out/nucleagent-executor .

# ---- Stage 3: 最终镜像（hermes 运行时 + Go 二进制，executor 作 PID 1）----
FROM hermes AS final

# 覆盖 hermes 的 ENV：HERMES_HOME 指向可写数据目录；PATH 保留 hermes venv。
ENV HERMES_HOME=/opt/data
ENV PYTHONUNBUFFERED=1
# HermesBackend 直接调 venv 真二进制（绕过需要 s6 的 docker shim）。
ENV HERMES_BIN=/opt/hermes/.venv/bin/hermes
ENV HERMES_WORKDIR=/opt/data
ENV HERMES_HOST=127.0.0.1

# 数据目录（hermes 会话/managed config/日志）。
RUN mkdir -p /opt/data

# 叠入 Go executor 二进制 + config.yaml（core.Viper 从 CWD 读 config.yaml）。
COPY --from=go-build /out/nucleagent-executor /usr/local/bin/nucleagent-executor
COPY nucleagent-executor/app/src/server/config.yaml /opt/config.yaml

WORKDIR /opt

# executor 作 PID 1：它自己拉起并守护 `hermes serve` 子进程。
# 不用 hermes 的 s6 ENTRYPOINT（/init + main-wrapper.sh）——那是给 hermes
# 单跑/gateway 模式用的；我们由 Go 侧 supervisor 全权管理 hermes 生命周期。
ENTRYPOINT ["/usr/local/bin/nucleagent-executor"]
CMD []
