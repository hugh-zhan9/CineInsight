#!/usr/bin/env python3
"""生成扩展图标。

不引第三方依赖：直接按 PNG 规范写 RGBA 位图。图案是一个圆角方块加一个向下的箭头，
四个尺寸同一套几何，按比例缩放，不做位图放大。
"""

import struct
import zlib
from pathlib import Path

BG = (47, 111, 235, 255)      # --accent
FG = (255, 255, 255, 255)
TRANSPARENT = (0, 0, 0, 0)


def rounded_rect_alpha(x, y, size, radius):
    """点 (x, y) 落在圆角方块内的覆盖度（0..1），四角做一点抗锯齿。"""
    left, top, right, bottom = 0.0, 0.0, float(size), float(size)
    cx = min(max(x, left + radius), right - radius)
    cy = min(max(y, top + radius), bottom - radius)
    dx, dy = x - cx, y - cy
    distance = (dx * dx + dy * dy) ** 0.5
    return max(0.0, min(1.0, radius - distance + 0.5))


def arrow_alpha(x, y, size):
    """向下的箭头：一根竖杆加一个三角头。"""
    unit = size / 16.0
    # 竖杆
    if abs(x - size / 2) <= 1.6 * unit and 3.4 * unit <= y <= 9.2 * unit:
        return 1.0
    # 三角头：宽度随 y 线性收窄
    head_top, head_bottom = 8.2 * unit, 12.4 * unit
    if head_top <= y <= head_bottom:
        progress = (y - head_top) / (head_bottom - head_top)
        half_width = 4.2 * unit * (1.0 - progress)
        if abs(x - size / 2) <= half_width:
            return 1.0
    # 底托
    if 13.0 * unit <= y <= 14.2 * unit and abs(x - size / 2) <= 4.6 * unit:
        return 1.0
    return 0.0


def blend(base, top, alpha):
    return tuple(round(base[i] * (1 - alpha) + top[i] * alpha) for i in range(4))


def render(size):
    radius = size * 0.22
    rows = []
    for py in range(size):
        row = bytearray()
        for px in range(size):
            x, y = px + 0.5, py + 0.5
            coverage = rounded_rect_alpha(x, y, size, radius)
            pixel = blend(TRANSPARENT, BG, coverage)
            mark = arrow_alpha(x, y, size)
            if mark > 0 and coverage > 0:
                pixel = blend(pixel, FG, mark * coverage)
            row.extend(pixel)
        rows.append(bytes(row))
    return rows


def write_png(path, size):
    rows = render(size)
    raw = b"".join(b"\x00" + row for row in rows)

    def chunk(tag, payload):
        body = tag + payload
        return struct.pack(">I", len(payload)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    png = b"\x89PNG\r\n\x1a\n"
    png += chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(raw, 9))
    png += chunk(b"IEND", b"")
    path.write_bytes(png)
    return len(png)


if __name__ == "__main__":
    here = Path(__file__).parent
    for size in (16, 32, 48, 128):
        written = write_png(here / f"icon{size}.png", size)
        print(f"icon{size}.png  {written} bytes")
