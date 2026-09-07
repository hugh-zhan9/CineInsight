#!/usr/bin/env python3
"""从 icon.svg 渲染扩展图标。

四档 PNG 都由同一份 icon.svg 用 Chrome 无头模式逐档渲染（矢量直接光栅化），不做位图缩放。
机器上要有 Chrome / Edge / Chromium，也可以用环境变量 CHROME 指定可执行文件。

已知的 Chrome 行为，脚本里都处理了：
- `--screenshot` 把文件写完后进程不会自己退出，所以轮询到输出文件大小稳定后主动结束整个进程组；
- 多个实例复用同一个 --user-data-dir 会互相等待卡死，所以每一档用独立的临时 profile。
"""

import os
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
SOURCE = HERE / "icon.svg"
SIZES = (16, 32, 48, 128)
TIMEOUT_SECONDS = 60

CHROME_CANDIDATES = (
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "google-chrome",
    "chromium",
    "chromium-browser",
    "microsoft-edge",
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
)


def find_chrome():
    override = os.environ.get("CHROME")
    if override:
        if Path(override).is_file():
            return override
        sys.exit(f"环境变量 CHROME 指向的文件不存在：{override}")
    for candidate in CHROME_CANDIDATES:
        if Path(candidate).is_file():
            return candidate
        found = shutil.which(candidate)
        if found:
            return found
    sys.exit("找不到 Chrome / Edge / Chromium；用环境变量 CHROME 指定可执行文件路径")


def start_render(chrome, size, workdir):
    """起一个无头 Chrome 把 SVG 按 size 截成透明底 PNG，返回 (进程, 输出路径)。"""
    page = workdir / f"page-{size}.html"
    page.write_text(
        "<!doctype html><meta charset=\"utf-8\">"
        "<style>html,body{margin:0;background:transparent;overflow:hidden}"
        f"img{{display:block;width:{size}px;height:{size}px}}</style>"
        f"<img src=\"{SOURCE.as_uri()}\">",
        encoding="utf-8",
    )
    out = workdir / f"icon{size}.png"
    cmd = [
        chrome,
        "--headless=new",
        "--disable-gpu",
        "--no-first-run",
        "--hide-scrollbars",
        f"--user-data-dir={workdir / f'profile-{size}'}",
        "--default-background-color=00000000",
        "--force-device-scale-factor=1",
        f"--window-size={size},{size}",
        f"--screenshot={out}",
        page.as_uri(),
    ]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=(os.name != "nt"),
    )
    return proc, out


def wait_until_written(proc, out):
    """输出文件出现且 0.5 秒内大小不再变化即视为写完；Chrome 自己退出了也算。"""
    deadline = time.monotonic() + TIMEOUT_SECONDS
    last_size = -1
    while time.monotonic() < deadline:
        if out.is_file() and out.stat().st_size > 0:
            size_now = out.stat().st_size
            if size_now == last_size:
                return True
            last_size = size_now
        elif proc.poll() is not None:
            return False
        time.sleep(0.5)
    return out.is_file() and out.stat().st_size > 0


def stop(proc):
    if proc.poll() is not None:
        return
    if os.name != "nt":
        os.killpg(proc.pid, signal.SIGTERM)
    else:
        proc.kill()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        if os.name != "nt":
            os.killpg(proc.pid, signal.SIGKILL)
        proc.wait()


def verify_png(path, size):
    data = path.read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n" or data[12:16] != b"IHDR":
        sys.exit(f"{path.name} 不是 PNG")
    width, height = struct.unpack(">II", data[16:24])
    if (width, height) != (size, size):
        sys.exit(f"{path.name} 尺寸是 {width}x{height}，期望 {size}x{size}")
    if data[25] != 6:
        sys.exit(f"{path.name} 颜色类型是 {data[25]}，期望 6（RGBA）")
    return len(data)


def main():
    if not SOURCE.is_file():
        sys.exit(f"缺少源文件 {SOURCE}")
    chrome = find_chrome()
    with tempfile.TemporaryDirectory(prefix="cineinsight-icons-") as tmp:
        workdir = Path(tmp)
        jobs = [(size, *start_render(chrome, size, workdir)) for size in SIZES]
        try:
            for size, proc, out in jobs:
                if not wait_until_written(proc, out):
                    sys.exit(f"渲染 {size}px 失败：Chrome 没有写出截图")
        finally:
            for _, proc, _ in jobs:
                stop(proc)
        for size, _, out in jobs:
            written = verify_png(out, size)
            shutil.copyfile(out, HERE / f"icon{size}.png")
            print(f"icon{size}.png  {written} bytes")


if __name__ == "__main__":
    main()
