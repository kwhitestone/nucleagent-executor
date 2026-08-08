#!/usr/bin/env bash
# build.sh — 构建 nucleagent-executor 单容器镜像（Go executor + Hermes Agent）。
#
# 两步构建（因 hermes-agent 与 executor 的 Docker build context 不同）：
#   1. docker build -t hermes-base hermes-agent/        # hermes 运行时
#   2. docker build -t nucleagent-executor -f Dockerfile <workspace-root>  # 叠 Go 二进制
#
# 用法：
#   ./build.sh                 # 默认：构建 hermes-base + nucleagent-executor
#   ./build.sh --skip-hermes   # 跳过 hermes-base（已构建过，只重建 executor）
#
# 镜像名可通过环境变量覆盖：HERMES_IMAGE / EXECUTOR_IMAGE。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"   # nucleagent-workspace 根
HERMES_IMAGE="${HERMES_IMAGE:-hermes-base}"
EXECUTOR_IMAGE="${EXECUTOR_IMAGE:-nucleagent-executor}"

SKIP_HERMES=0
for arg in "$@"; do
  case "$arg" in
    --skip-hermes) SKIP_HERMES=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

echo "==> workspace root: $WORKSPACE_ROOT"
echo "==> hermes-agent:   $SCRIPT_DIR/hermes-agent"

# ---- Step 1: hermes-base（首次构建较慢：uv + npm + playwright）------------
if [ "$SKIP_HERMES" -eq 0 ]; then
  if ! docker image inspect "$HERMES_IMAGE" >/dev/null 2>&1; then
    echo "==> building $HERMES_IMAGE from hermes-agent/"
    docker build -t "$HERMES_IMAGE" "$SCRIPT_DIR/hermes-agent/"
  else
    echo "==> $HERMES_IMAGE 已存在，跳过（删掉该镜像可强制重建）"
  fi
else
  echo "==> --skip-hermes：假定 $HERMES_IMAGE 已存在"
  if ! docker image inspect "$HERMES_IMAGE" >/dev/null 2>&1; then
    echo "错误：$HERMES_IMAGE 不存在，去掉 --skip-hermes 先构建它" >&2
    exit 1
  fi
fi

# ---- Step 2: nucleagent-executor（叠 Go 二进制，快）----------------------
echo "==> building $EXECUTOR_IMAGE (context=$WORKSPACE_ROOT)"
docker build -t "$EXECUTOR_IMAGE" -f "$SCRIPT_DIR/Dockerfile" "$WORKSPACE_ROOT"

echo "==> 完成：$EXECUTOR_IMAGE"
echo "    运行：docker run --rm -p 26690:26690 $EXECUTOR_IMAGE"
