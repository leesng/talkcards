#!/usr/bin/env bash
# 构建单二进制后端（前端已嵌入），产物输出到仓库根 target/talkcards-{goos}-{goarch}。
# 默认 linux/amd64 + linux/arm64；
# 用法：./build.sh [goos] [goarch]，如 ./build.sh linux arm64
#       ./build.sh run [go run 额外参数...]  不构建二进制，直接 go run 起服务，
#                           前端静态资源用磁盘上的 web/static（--static-dir），便于改前端即刷即用
#       ./build.sh test    构建本机架构二进制并跑完整对局审计（scripts/e2e-audit.js）
#                           端口可用 TEST_PORT 覆盖，默认 8765
set -euo pipefail
cd "$(dirname "$0")/backend"

# run 子命令：不构建二进制，go run 直启；静态资源走磁盘 web/static（非嵌入）
if [[ "${1:-}" == "run" ]]; then
  shift
  exec go run ./cmd/server --static-dir ./web/static "$@"
fi

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
LDFLAGS="-s -w -X main.version=${VERSION}"

# 门禁：静态检查 + 单测
go vet ./...
go test ./... -count=1

# test 子命令：构建本机架构 → 临时起服务 → 完整对局审计（确定性出牌 + 重放校验）
if [[ "${1:-}" == "test" ]]; then
  goos="$(go env GOOS)"
  goarch="$(go env GOARCH)"
  TARGET_DIR="$(cd .. && pwd)/target"
  mkdir -p "${TARGET_DIR}"
  out="${TARGET_DIR}/talkcards-${goos}-${goarch}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" ./cmd/server
  echo "target/talkcards-${goos}-${goarch}  $(du -h "${out}" | cut -f1)  (${VERSION})"

  PORT="${TEST_PORT:-8765}"
  SRV_LOG="$(mktemp /tmp/gtp-audit-server.XXXXXX.log)"
  SRV_DIR="$(mktemp -d /tmp/gtp-audit-srv.XXXXXX)" # 服务在临时目录运行，默认 ./talkcards.db 不污染仓库
  # --reconnect-timeout 5：让 e2e 的"掉线超时判逃跑"场景可测
  (cd "${SRV_DIR}" && PORT="${PORT}" exec "${out}" --reconnect-timeout 5) >"${SRV_LOG}" 2>&1 &
  SRV_PID=$!
  trap 'kill "${SRV_PID}" 2>/dev/null || true; wait "${SRV_PID}" 2>/dev/null || true' EXIT

  # 等服务就绪（进程死掉立即报错，防止误连端口上残留的旧实例）
  ready=0
  for _ in $(seq 1 50); do
    if ! kill -0 "${SRV_PID}" 2>/dev/null; then
      echo "ERROR: 服务进程启动即退出（端口 ${PORT} 被占用？），日志：" >&2
      cat "${SRV_LOG}" >&2
      exit 1
    fi
    if curl -sf "http://127.0.0.1:${PORT}/" >/dev/null; then ready=1; break; fi
    sleep 0.1
  done
  if [[ ${ready} -ne 1 ]]; then
    echo "ERROR: 服务 ${PORT} 就绪超时（日志：${SRV_LOG}）" >&2
    exit 1
  fi

  echo "—— E2E 验收（http://127.0.0.1:${PORT}，服务日志 ${SRV_LOG}）——"
  timeout 180 node scripts/e2e-bot.js "http://127.0.0.1:${PORT}"
  # 审计为随机炸弹策略（有分 90%/无分 10%），多局连跑覆盖不同随机序列
  ROUNDS="${AUDIT_ROUNDS:-3}"
  for i in $(seq 1 "${ROUNDS}"); do
    echo "—— 完整对局审计（http://127.0.0.1:${PORT}）第 ${i}/${ROUNDS} 局 ——"
    timeout 180 node scripts/e2e-audit.js "http://127.0.0.1:${PORT}"
  done
  echo "验收与审计全部通过"
  exit 0
fi

targets=()
if [[ $# -ge 2 ]]; then
  targets=("$1/$2")
else
  targets=(linux/amd64 linux/arm64)
fi

# 产物输出到仓库根 target/，按平台命名 talkcards-{goos}-{goarch}
TARGET_DIR="$(cd .. && pwd)/target"
mkdir -p "${TARGET_DIR}"
for t in "${targets[@]}"; do
  goos="${t%/*}"
  goarch="${t#*/}"
  out="${TARGET_DIR}/talkcards-${goos}-${goarch}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" ./cmd/server
  echo "target/talkcards-${goos}-${goarch}  $(du -h "${out}" | cut -f1)  (${VERSION})"
done
