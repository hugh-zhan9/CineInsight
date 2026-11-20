#!/usr/bin/env python3
"""Prepare local Apple Translation resources before replacing the macOS app."""

import argparse
import json
from pathlib import Path
import subprocess
import sys
import time


REQUIRED_PAIRS = (("ja", "zh"), ("ko", "zh"), ("en", "zh"))
PAIR_TIMEOUT = 600
POLL_SECONDS = 2


def helper_request(helper, operation, source, target, timeout):
    pair = f"{source}→{target}"
    try:
        process = subprocess.run(
            [helper],
            input=json.dumps({"operation": operation, "source": source, "target": target}),
            capture_output=True,
            text=True,
            timeout=timeout,
            check=True,
        )
        response = json.loads(process.stdout)
    except subprocess.TimeoutExpired as error:
        raise RuntimeError(f"{pair}：等待系统翻译资源超时") from error
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        raise RuntimeError(f"{pair}：无法读取系统字幕组件结果") from error
    if not isinstance(response, dict):
        raise RuntimeError(f"{pair}：系统字幕组件返回的结果无效")
    error = response.get("error")
    if error == "apple_invalid_request" and operation == "translation_status":
        raise RuntimeError(f"{pair}：系统字幕组件可能是旧版本，请去掉 --skip-build 重新构建")
    if error == "cancelled":
        raise RuntimeError(f"{pair}：已取消准备系统翻译资源")
    if error:
        raise RuntimeError(f"{pair}：系统翻译资源准备失败（{error}）")
    return response


def pair_status(helper, source, target, timeout):
    status = helper_request(helper, "translation_status", source, target, timeout).get("translationStatus")
    if status == "unsupported":
        raise RuntimeError(f"{source}→{target}：当前 macOS 不支持此系统翻译语言对")
    if status not in ("installed", "supported"):
        raise RuntimeError(f"{source}→{target}：无法确认系统翻译资源状态，请使用当前版本组件重新构建")
    return status


def prepare_pairs(helper, timeout=PAIR_TIMEOUT, poll=POLL_SECONDS):
    for source, target in REQUIRED_PAIRS:
        pair = f"{source}→{target}"
        if pair_status(helper, source, target, min(timeout, 30)) == "installed":
            print(f"    {pair}：已安装", flush=True)
            continue
        print(f"    {pair}：缺少翻译资源，请按 macOS 窗口提示确认下载", flush=True)
        deadline = time.monotonic() + timeout
        result = helper_request(helper, "prepare_translation", source, target, timeout)
        if result.get("supported") is not True:
            raise RuntimeError(f"{pair}：系统字幕组件未确认准备成功")
        # Apple may return from prepareTranslation while a download is already underway.
        while (remaining := deadline - time.monotonic()) > 0:
            if pair_status(helper, source, target, min(remaining, 30)) == "installed":
                print(f"    {pair}：已确认安装完成", flush=True)
                break
            time.sleep(min(poll, max(0, deadline - time.monotonic())))
        else:
            raise RuntimeError(f"{pair}：超时，尚未核实系统翻译资源已安装")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--helper", required=True, type=Path)
    args = parser.parse_args()
    try:
        prepare_pairs(str(args.helper.resolve()))
    except (RuntimeError, KeyboardInterrupt) as error:
        print(str(error) or "已中断系统翻译资源准备", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
