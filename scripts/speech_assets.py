#!/usr/bin/env python3
"""Helpers for install_speech_assets.sh: read Apple speech asset state and edit the input-source list.

macOS 15 has no download API for SFSpeechRecognizer on-device assets. The system fetches
`com.apple.speech.asr.transcription.<lang>` (type `com.apple.MobileAsset.UAF.Speech.AutomaticSpeechRecognition`)
for the languages registered as dictation languages, and those follow the enabled input sources in
com.apple.HIToolbox. This module only reads status, reports what is missing and produces a modified
preference list; the calling script owns backup, import, waiting and exact restore.
"""
import argparse
import json
import plistlib
import subprocess
import sys

# Input sources that make macOS treat the language as a dictation language. Each input mode comes with
# its "Keyboard Input Method" owner entry, the same shape System Settings writes.
INPUT_SOURCES = {
    "ja": [
        {
            "Bundle ID": "com.apple.inputmethod.Kotoeri.RomajiTyping",
            "Input Mode": "com.apple.inputmethod.Kotoeri.RomajiTyping.Japanese",
            "InputSourceKind": "Input Mode",
        },
        {
            "Bundle ID": "com.apple.inputmethod.Kotoeri.RomajiTyping",
            "InputSourceKind": "Keyboard Input Method",
        },
    ],
    "ko": [
        {
            "Bundle ID": "com.apple.inputmethod.Korean",
            "Input Mode": "com.apple.inputmethod.Korean.2SetKorean",
            "InputSourceKind": "Input Mode",
        },
        {
            "Bundle ID": "com.apple.inputmethod.Korean",
            "InputSourceKind": "Keyboard Input Method",
        },
    ],
}

# Languages that normally arrive with the system (ABC layout, Pinyin and Mandarin/Cantonese modes).
SYSTEM_DEFAULT = {"en", "zh", "yue"}


def read_status(helper):
    """One `status` round trip against the applesystem helper; raises ValueError with a readable reason."""
    try:
        proc = subprocess.run(
            [helper],
            input=json.dumps({"operation": "status"}),
            capture_output=True,
            text=True,
            timeout=180,
        )
    except OSError as error:
        raise ValueError(f"无法运行识别组件：{error}") from error
    except subprocess.TimeoutExpired as error:
        raise ValueError("识别组件读取状态超时") from error
    line = next((item for item in proc.stdout.splitlines() if item.strip()), "")
    if not line:
        detail = proc.stderr.strip().splitlines()[-1] if proc.stderr.strip() else f"退出码 {proc.returncode}"
        raise ValueError(f"识别组件没有返回状态：{detail}")
    status = json.loads(line)
    if status.get("error"):
        raise ValueError(f"系统识别状态不可用（{status['error']}）")
    return status


def languages_of(status):
    return status.get("languages") or []


def is_installed(codes, wanted):
    return any(code.lower().split("-")[0] == wanted.lower() for code in codes)


def missing_languages(status, requested):
    codes = [item.get("code", "") for item in languages_of(status) if item.get("installed")]
    return [wanted for wanted in requested if not is_installed(codes, wanted)]


def cmd_status(args):
    status = read_status(args.helper)
    installed = [item.get("code", "") for item in languages_of(status) if item.get("installed")]
    for wanted in args.languages:
        matched = [item.get("code", "") for item in languages_of(status) if item.get("code", "").lower().split("-")[0] == wanted]
        if not matched:
            print(f"{wanted}: 系统不支持该语种")
        elif is_installed(installed, wanted):
            codes = ",".join(code for code in matched if code in installed)
            print(f"{wanted}: 本机可用（{codes}）")
        else:
            print(f"{wanted}: 未安装")
    return 0


def cmd_missing(args):
    status = read_status(args.helper)
    print(" ".join(missing_languages(status, args.languages)))
    return 0


def cmd_apply(args):
    with open(args.input, "rb") as handle:
        prefs = plistlib.load(handle)
    enabled = prefs.setdefault("AppleEnabledInputSources", [])
    added = []
    unsupported = []
    for wanted in args.languages:
        entries = INPUT_SOURCES.get(wanted)
        if not entries:
            unsupported.append(wanted)
            continue
        known = {(entry.get("Bundle ID"), entry.get("Input Mode")) for entry in enabled}
        new = [entry for entry in entries if (entry.get("Bundle ID"), entry.get("Input Mode")) not in known]
        if new:
            enabled.extend(new)
            added.append(wanted)
    if unsupported:
        print("不支持自动添加输入源：" + ",".join(unsupported), file=sys.stderr)
    with open(args.output, "wb") as handle:
        plistlib.dump(prefs, handle, fmt=plistlib.FMT_BINARY)
    print(" ".join(added))
    return 0 if added or not unsupported else 4


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    def common(sub):
        sub.add_argument("--helper", required=True, help="path to the cineinsight-apple-ai helper")
        sub.add_argument("--languages", required=True, help="comma separated language prefixes, e.g. ja,ko")

    status_parser = subparsers.add_parser("status", help="print per-language availability")
    common(status_parser)
    status_parser.set_defaults(func=cmd_status)

    missing_parser = subparsers.add_parser("missing", help="print the languages that are not installed yet")
    common(missing_parser)
    missing_parser.set_defaults(func=cmd_missing)

    apply_parser = subparsers.add_parser("apply", help="write a preference copy with the needed input sources")
    apply_parser.add_argument("--input", required=True, help="current com.apple.HIToolbox export")
    apply_parser.add_argument("--output", required=True, help="where to write the modified copy")
    apply_parser.add_argument("--languages", required=True, help="languages to add as dictation languages")
    apply_parser.set_defaults(func=cmd_apply)

    args = parser.parse_args(argv)
    if hasattr(args, "languages") and isinstance(args.languages, str):
        args.languages = [item.strip() for item in args.languages.split(",") if item.strip()]
    try:
        return args.func(args)
    except ValueError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
