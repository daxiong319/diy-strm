#!/usr/bin/env bash
#
# 构建 linux/amd64 运行时镜像（供飞牛 NAS 5211 使用）。
#
# 为什么不直接 `docker build --platform linux/amd64`：
#   本机是 aarch64，NAS 是 amd64，跨架构构建会启用 QEMU 模拟。Node 20 在 QEMU
#   下必然崩溃（Fatal process OOM in Failed to reserve virtual memory for
#   CodeRange / qemu: uncaught target signal 5），连 `node -e 1` 都挂死。
#   Go 则不受影响，因为它自带交叉编译，完全不经过 QEMU。
#
# 所以本脚本分三步，全程不依赖 QEMU 跑 Node：
#   1. 前端在本机原生构建（vite 产物直接写进 internal/api/web，由 go:embed 打包）
#   2. Go 用 GOOS=linux GOARCH=amd64 交叉编译成静态 ELF
#   3. 用 Dockerfile.runtime 把二进制装进 amd64 运行时镜像
#
# 用法：
#   scripts/make-amd64-image.sh [镜像名:标签]
# 默认镜像名 litepan-go:muvyo-port
set -euo pipefail

IMAGE="${1:-litepan-go:muvyo-port}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export PATH="$PATH:/usr/local/go/bin"
export GOWORK=off

echo "==> [1/3] 构建前端（本机原生，避免 QEMU）"
cd "$ROOT/web"
if [ ! -d node_modules ]; then
  echo "    node_modules 缺失，先 npm ci"
  npm ci
fi
npm run build
cd "$ROOT"

if [ ! -f internal/api/web/index.html ]; then
  echo "!! 前端产物缺失：internal/api/web/index.html 不存在" >&2
  exit 1
fi

echo "==> [2/3] 交叉编译 linux/amd64 静态二进制"
mkdir -p dist
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -tags fuse -trimpath -ldflags="-s -w" -o dist/litepan-amd64 ./cmd/litepan

if ! file dist/litepan-amd64 | grep -q "x86-64"; then
  echo "!! 产物不是 x86-64：" >&2
  file dist/litepan-amd64 >&2
  exit 1
fi
ls -lh dist/litepan-amd64

echo "==> [3/3] 组装运行时镜像 $IMAGE"
docker build --platform linux/amd64 -f Dockerfile.runtime -t "$IMAGE" .

echo "==> 校验镜像架构与内嵌前端"
ARCH="$(docker image inspect "$IMAGE" --format '{{.Architecture}}')"
if [ "$ARCH" != "amd64" ]; then
  echo "!! 镜像架构不是 amd64，而是 $ARCH" >&2
  exit 1
fi
# 校验前端确实被打进了二进制：找一个只有本次新增页面才有的 chunk 名。
if ! strings dist/litepan-amd64 | grep -q "McpSettings"; then
  echo "!! 二进制内未找到 MCP 前端资源，go:embed 可能没取到最新前端产物" >&2
  exit 1
fi

echo "完成：$IMAGE"
docker images "$IMAGE" --format "{{.Repository}}:{{.Tag}}  {{.ID}}  {{.Size}}"
