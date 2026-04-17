#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist/release"
APP_NAME="cloudnest"

VERSION="${1:-}"
if [[ -z "${VERSION}" ]]; then
  if git -C "${ROOT_DIR}" rev-parse --git-dir >/dev/null 2>&1; then
    VERSION="$(git -C "${ROOT_DIR}" describe --tags --always --dirty 2>/dev/null || true)"
  fi
fi
if [[ -z "${VERSION}" ]]; then
  VERSION="v$(date +%Y%m%d-%H%M%S)"
fi

if git -C "${ROOT_DIR}" rev-parse --git-dir >/dev/null 2>&1; then
  COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
else
  COMMIT="unknown"
fi
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

mkdir -p "${DIST_DIR}"

platforms=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
)

echo "[release] version=${VERSION} commit=${COMMIT} build_time=${BUILD_TIME}"

for item in "${platforms[@]}"; do
  GOOS="${item%% *}"
  GOARCH="${item##* }"
  PKG_DIR="${DIST_DIR}/${APP_NAME}_${VERSION}_${GOOS}_${GOARCH}"
  TAR_NAME="${APP_NAME}_${VERSION}_${GOOS}_${GOARCH}.tar.gz"

  mkdir -p "${PKG_DIR}"

  (cd "${ROOT_DIR}" && \
    GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath \
      -ldflags "-s -w -X 'main.buildVersion=${VERSION}' -X 'main.buildCommit=${COMMIT}' -X 'main.buildTime=${BUILD_TIME}'" \
      -o "${PKG_DIR}/${APP_NAME}" ./cmd/server)

  cp "${ROOT_DIR}/docs/部署打包与命令行启动.md" "${PKG_DIR}/"
  cp "${ROOT_DIR}/.env.example" "${PKG_DIR}/.env.example"

  (cd "${DIST_DIR}" && tar -czf "${TAR_NAME}" "$(basename "${PKG_DIR}")")
  rm -rf "${PKG_DIR}"

  echo "[release] created ${DIST_DIR}/${TAR_NAME}"
done

echo "[release] done"
