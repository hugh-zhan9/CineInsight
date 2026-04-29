#!/usr/bin/env bash

set -euo pipefail

APP_NAME="析微影策"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_APP_PATH="${PROJECT_ROOT}/build/bin/${APP_NAME}.app"
INSTALL_APP_PATH="/Applications/${APP_NAME}.app"
TMP_DIR_ROOT="${TMPDIR:-/tmp}"
SKIP_BUILD=0
WAILS_ARGS=()

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
  $(basename "$0") [--skip-build] [wails build 参数...]

说明:
  1. 在项目根目录执行 wails build
  2. 产出 ${BUILD_APP_PATH}
  3. 关闭正在运行的 ${APP_NAME}
  4. 将新包替换到 ${INSTALL_APP_PATH}

示例:
  $(basename "$0")
  $(basename "$0") -clean
  $(basename "$0") --skip-build
EOF
}

ensure_macos() {
  [[ "$(uname -s)" == "Darwin" ]] || fail "该脚本仅支持 macOS。"
}

resolve_wails() {
  if command -v wails >/dev/null 2>&1; then
    command -v wails
    return
  fi

  if [[ -x "${HOME}/go/bin/wails" ]]; then
    printf '%s\n' "${HOME}/go/bin/wails"
    return
  fi

  fail "未找到 wails CLI，请先安装并确保 PATH 可用，或确认 ${HOME}/go/bin/wails 存在。"
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
  local wails_bin="$1"
  local build_ldflags="${CGO_LDFLAGS:-}"

  if (( SKIP_BUILD == 1 )); then
    log "跳过构建，直接使用现有产物"
    return
  fi

  if [[ "${build_ldflags}" != *"UniformTypeIdentifiers"* ]]; then
    build_ldflags="${build_ldflags:+${build_ldflags} }-framework UniformTypeIdentifiers"
  fi

  log "开始执行 Wails 构建"
  (
    cd "${PROJECT_ROOT}"
    export CGO_LDFLAGS="${build_ldflags}"
    if [[ "$(declare -p WAILS_ARGS 2>/dev/null || true)" == "declare -a"* ]] && (( ${#WAILS_ARGS[@]} > 0 )); then
      "${wails_bin}" build "${WAILS_ARGS[@]}"
    else
      "${wails_bin}" build
    fi
  )
}

wait_for_app_exit() {
  local retries=0

  while pgrep -x "${APP_NAME}" >/dev/null 2>&1; do
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

  log "复制新包到临时目录"
  ditto "${BUILD_APP_PATH}" "${temp_app}"

  log "停止正在运行的旧应用"
  pkill -x "${APP_NAME}" 2>/dev/null || true
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
  log "启动已安装的新应用"
  open -n -a "${INSTALL_APP_PATH}"
}

main() {
  local wails_bin

  ensure_macos

  while (( $# > 0 )); do
    case "$1" in
      --skip-build)
        SKIP_BUILD=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        WAILS_ARGS+=("$1")
        shift
        ;;
    esac
  done

  wails_bin="$(resolve_wails)"
  build_app "${wails_bin}"

  [[ -d "${BUILD_APP_PATH}" ]] || fail "未找到构建产物: ${BUILD_APP_PATH}"

  replace_installed_app
  launch_installed_app
  log "完成，已替换 ${INSTALL_APP_PATH}"
}

main "$@"
