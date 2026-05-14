#!/usr/bin/env bash

set -euo pipefail

APP_NAME="析微影策"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_APP_PATH="${PROJECT_ROOT}/dist/native-dev/CineInsightNative.app"
INSTALL_APP_PATH="/Applications/${APP_NAME}.app"
TMP_DIR_ROOT="${TMPDIR:-/tmp}"
SKIP_BUILD=0
LAUNCH_AFTER_INSTALL=1

log() {
  printf '==> %s\n' "$*"
}

fail() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<EOF
用法:
  $(basename "$0") [--skip-build] [--no-launch]

说明:
  1. 默认执行 scripts/package_native_dev.sh 构建 Rust/SwiftUI 原生包
  2. 产出 ${BUILD_APP_PATH}
  3. 关闭正在运行的 ${APP_NAME}
  4. 将新包替换到 ${INSTALL_APP_PATH}

示例:
  $(basename "$0")
  $(basename "$0") --skip-build
  $(basename "$0") --no-launch
EOF
}

ensure_macos() {
  [[ "$(uname -s)" == "Darwin" ]] || fail "该脚本仅支持 macOS。"
}

should_use_sudo() {
  [[ ! -w "/Applications" ]] || [[ -e "${INSTALL_APP_PATH}" && ! -w "${INSTALL_APP_PATH}" ]]
}

run_install_cmd() {
  if should_use_sudo; then
    sudo "$@"
  else
    "$@"
  fi
}

build_app() {
  if (( SKIP_BUILD == 1 )); then
    log "跳过构建，直接使用现有 native 产物"
    return
  fi

  log "开始构建 Rust/SwiftUI native 包"
  bash "${PROJECT_ROOT}/scripts/package_native_dev.sh" >/dev/null
}

wait_for_app_exit() {
  local retries=0

  while pgrep -x "${APP_NAME}" >/dev/null 2>&1 || pgrep -x "CineInsightNative" >/dev/null 2>&1; do
    if (( retries >= 20 )); then
      fail "${APP_NAME} 仍未退出，请手动关闭后重试。"
    fi
    sleep 1
    retries=$((retries + 1))
  done
}

replace_installed_app() {
  local temp_dir temp_app backup_app

  temp_dir="$(mktemp -d "${TMP_DIR_ROOT%/}/${APP_NAME}.install.XXXXXX")"
  temp_app="${temp_dir}/${APP_NAME}.app"
  backup_app="${temp_dir}/${APP_NAME}.app.backup"
  trap "rm -rf -- \"${temp_dir}\"" EXIT

  log "复制 native 包到临时目录"
  ditto "${BUILD_APP_PATH}" "${temp_app}"

  log "停止正在运行的旧应用"
  pkill -x "${APP_NAME}" 2>/dev/null || true
  pkill -x "CineInsightNative" 2>/dev/null || true
  wait_for_app_exit

  if [[ -e "${INSTALL_APP_PATH}" ]]; then
    log "备份旧应用"
    run_install_cmd mv "${INSTALL_APP_PATH}" "${backup_app}"
  fi

  log "安装新应用到 /Applications"
  if ! run_install_cmd ditto "${temp_app}" "${INSTALL_APP_PATH}"; then
    if [[ -e "${backup_app}" ]]; then
      log "安装失败，恢复旧应用"
      run_install_cmd rm -rf "${INSTALL_APP_PATH}"
      run_install_cmd mv "${backup_app}" "${INSTALL_APP_PATH}"
    fi
    fail "安装新应用失败。"
  fi

  if [[ -e "${backup_app}" ]]; then
    run_install_cmd rm -rf "${backup_app}"
  fi

  trap - EXIT
  rm -rf "${temp_dir}"
}

launch_installed_app() {
  if (( LAUNCH_AFTER_INSTALL == 1 )); then
    log "启动已安装的新应用"
    open -n "${INSTALL_APP_PATH}"
  fi
}

main() {
  ensure_macos

  while (( $# > 0 )); do
    case "$1" in
      --skip-build)
        SKIP_BUILD=1
        shift
        ;;
      --no-launch)
        LAUNCH_AFTER_INSTALL=0
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        fail "未知参数: $1"
        ;;
    esac
  done

  build_app

  [[ -d "${BUILD_APP_PATH}" ]] || fail "未找到 native 构建产物: ${BUILD_APP_PATH}"
  [[ -x "${BUILD_APP_PATH}/Contents/MacOS/CineInsightNative" ]] || fail "native 可执行文件不可用: ${BUILD_APP_PATH}"

  replace_installed_app
  launch_installed_app
  log "完成，已替换 ${INSTALL_APP_PATH}"
}

main "$@"
