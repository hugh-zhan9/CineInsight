#!/usr/bin/env bash
# 触发 macOS 下载并安装本机语音识别资源（SFSpeechRecognizer 离线模型，日/韩等）。
#
# 为什么需要这个脚本：macOS 15 没有给第三方任何「下载识别模型」的接口，系统只为已注册成
# 听写语言的语言去下载 `com.apple.speech.asr.transcription.<lang>` 资产，而听写语言跟随
# com.apple.HIToolbox 里启用的输入源。脚本临时把对应输入源写进系统偏好，等模型装好后
# 原样还原，不保留任何输入源改动；应用侧行为、隐私承诺（音频不出本机）都不变。
#
# 用法：bash scripts/install_speech_assets.sh [--languages ja,ko,en,zh] [--status] [--dry-run]
#                                              [--helper PATH] [--timeout 秒]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PY="${SCRIPT_DIR}/speech_assets.py"
APP_NAME="析微影策"
DEFAULT_LANGUAGES="ja,ko,en,zh"
WAIT_SECONDS="${CINEINSIGHT_SPEECH_ASSET_WAIT:-300}"
POLL_SECONDS=5

LANGUAGES="${DEFAULT_LANGUAGES}"
HELPER=""
STATUS_ONLY=0
DRY_RUN=0

log() { printf '==> %s\n' "$*"; }
warn() { printf '提示：%s\n' "$*" >&2; }
fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
用法: bash scripts/install_speech_assets.sh [选项]

触发 macOS 下载并安装本机语音识别资源（离线模型）。默认处理 ja,ko,en,zh：
已安装的跳过，缺的临时注册成听写语言让系统去下载，结束后还原输入源设置。

选项:
  --languages ja,ko   只处理这些语言前缀（默认 ja,ko,en,zh）
  --status            只打印当前各语言是否本机可用，不改动任何设置
  --dry-run           只打印缺哪些语言、将要临时加哪些输入源
  --helper PATH       指定 cineinsight-apple-ai 组件路径（默认自动查找）
  --timeout 秒        等待系统下载的上限，默认 300
  -h, --help          显示本帮助

说明: macOS 只为「已注册成听写语言」的语言下载识别模型，听写语言跟随启用的输入源。
脚本支持的输入源: ja（Kotoeri 罗马字）、ko（2-Set Korean）；en/zh/yue 一般随系统自带，
缺失时需要先在系统设置里手动添加对应输入源。
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --languages) [[ $# -ge 2 ]] || fail "--languages 需要参数"; LANGUAGES="$2"; shift 2 ;;
    --languages=*) LANGUAGES="${1#*=}"; shift ;;
    --helper) [[ $# -ge 2 ]] || fail "--helper 需要参数"; HELPER="$2"; shift 2 ;;
    --helper=*) HELPER="${1#*=}"; shift ;;
    --timeout) [[ $# -ge 2 ]] || fail "--timeout 需要参数"; WAIT_SECONDS="$2"; shift 2 ;;
    --timeout=*) WAIT_SECONDS="${1#*=}"; shift ;;
    --status) STATUS_ONLY=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "未知参数 $1" ;;
  esac
done

[[ "$(uname -s)" == Darwin ]] || fail "本机语音识别资源只在 macOS 上可用"
command -v python3 >/dev/null 2>&1 || fail "需要 python3"

resolve_helper() {
  if [[ -n "${HELPER}" ]]; then
    [[ -x "${HELPER}" ]] || fail "指定的识别组件不可执行：${HELPER}"
    return
  fi
  local candidates=(
    "/Applications/${APP_NAME}.app/Contents/Resources/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai"
    "${PROJECT_ROOT}/build/bin/${APP_NAME}.app/Contents/Resources/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai"
    "${PROJECT_ROOT}/build/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai"
  )
  for candidate in "${candidates[@]}"; do
    if [[ -x "${candidate}" ]]; then HELPER="${candidate}"; return; fi
  done
  fail "找不到合成的系统识别组件。先安装应用（bash scripts/build_and_install_app.sh）或用 --helper 指定。"
}
resolve_helper
log "识别组件：${HELPER}"

if (( STATUS_ONLY )); then
  python3 "${PY}" status --helper "${HELPER}" --languages "${LANGUAGES}"
  exit $?
fi

missing="$(python3 "${PY}" missing --helper "${HELPER}" --languages "${LANGUAGES}")" || exit $?
if [[ -z "${missing}" ]]; then
  log "指定的语言都已是本机可用：${LANGUAGES}"
  exit 0
fi
log "本机尚未安装：${missing}"

if (( DRY_RUN )); then
  tmp="$(mktemp -t cineinsight-hitoolbox)"
  trap 'rm -f "${tmp}"' EXIT
  defaults export com.apple.HIToolbox "${tmp}"
  added="$(python3 "${PY}" apply --input "${tmp}" --output "${tmp}.patched" --languages "${missing// /,}" || true)"
  log "会临时添加输入源：${added:-无（这些语言缺少可自动添加的输入源，需要手动在系统设置里加）}"
  log "dry-run：没有改动任何设置"
  exit 0
fi

backup="$(mktemp -t cineinsight-hitoolbox-backup)"
patched="$(mktemp -t cineinsight-hitoolbox-patched)"
restored=0
restore_input_sources() {
  (( restored )) && return 0
  restored=1
  defaults import com.apple.HIToolbox "${backup}" >/dev/null 2>&1 || warn "还原输入源设置失败，备份仍在 ${backup}"
  killall TextInputMenuAgent imklaunchagent 2>/dev/null || true
  rm -f "${patched}"
}
cleanup() { restore_input_sources; }
trap cleanup EXIT INT TERM

defaults export com.apple.HIToolbox "${backup}"
added="$(python3 "${PY}" apply --input "${backup}" --output "${patched}" --languages "${missing// /,}")" || {
  restore_input_sources
  warn "${added:-没有可自动添加的输入源}"
  fail "这些语言无法自动触发下载：${missing}。请在系统设置 › 键盘 › 输入法里添加对应输入源后再运行本脚本。"
}
if [[ -z "${added}" ]]; then
  restore_input_sources
  fail "没有可自动添加的输入源：${missing}"
fi
log "临时注册为听写语言：${added}（结束后会还原）"
defaults import com.apple.HIToolbox "${patched}"
killall TextInputMenuAgent imklaunchagent 2>/dev/null || true

log "等待 macOS 下载并安装识别模型，最长 ${WAIT_SECONDS} 秒……"
deadline=$(( $(date +%s) + WAIT_SECONDS ))
last=""
while :; do
  still="$(python3 "${PY}" missing --helper "${HELPER}" --languages "${added// /,}")" || { warn "读取状态失败，停止等待"; break; }
  if [[ -z "${still}" ]]; then break; fi
  if [[ "${still}" != "${last}" ]]; then log "还在等：${still}"; last="${still}"; fi
  if (( $(date +%s) >= deadline )); then warn "等待超时，仍未安装：${still}"; break; fi
  sleep "${POLL_SECONDS}"
done

restore_input_sources
log "输入源设置已还原"
echo
log "当前状态："
python3 "${PY}" status --helper "${HELPER}" --languages "${LANGUAGES}"
remaining="$(python3 "${PY}" missing --helper "${HELPER}" --languages "${added// /,}")" || exit $?
if [[ -n "${remaining}" ]]; then
  cat >&2 <<EOF
提示: 仍然缺少 ${remaining}。可再运行一次本脚本；若多次无进展，用系统设置 › 键盘 › 听写
      手动添加该语言并真正听写一次，然后运行 --status 复查。
EOF
  exit 1
fi
log "完成：${added} 现在都是本机可用，应用里点「刷新状态」即可选到。"
