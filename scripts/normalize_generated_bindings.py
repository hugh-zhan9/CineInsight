#!/usr/bin/env python3
"""Remove generator-added whitespace-only lines without reformatting old bindings.

Run after `wails generate module` / `wails build`. This changes no declarations;
Wails remains the source of every binding. Existing unchanged lines are preserved.
"""
import pathlib
import re
import subprocess
import sys


def normalize_added_blank_lines(text, patch):
    lines = text.splitlines(keepends=True)
    position = None
    for line in patch.splitlines():
        match = re.match(r'^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@', line)
        if match:
            position = int(match.group(1)) - 1
        elif position is not None and line.startswith('+') and not line.startswith('+++'):
            if position < 0 or position >= len(lines) or lines[position].rstrip('\r\n') != line[1:]:
                raise ValueError('Generated output changed while formatting; regenerate and rerun')
            if not line[1:].strip():
                lines[position] = '\r\n' if lines[position].endswith('\r\n') else '\n'
            position += 1
        elif position is not None and line.startswith(' '):
            position += 1
    return ''.join(lines)


def main():
    if len(sys.argv) != 1:
        print('Usage: normalize_generated_bindings.py', file=sys.stderr)
        return 2
    root = pathlib.Path(__file__).resolve().parents[1]
    name = 'frontend/wailsjs/go/models.ts'
    path = root / name
    before = path.read_text()
    patch = subprocess.run(['git', '-C', str(root), 'diff', '--no-ext-diff', '--unified=0', '--', name], check=True, capture_output=True, text=True).stdout
    after = normalize_added_blank_lines(before, patch)
    if path.read_text() != before:
        raise ValueError('Generated output changed while formatting')
    if after != before:
        path.write_text(after)
    # Wails 2.11 rewrites these runtime files with executable bits although their
    # contents are unchanged. Restore the repository's ordinary source-file mode.
    for name in ['package.json', 'runtime.d.ts', 'runtime.js']:
        (root / 'frontend/wailsjs/runtime' / name).chmod(0o644)
    print('Generated binding whitespace normalized' if after != before else 'Generated binding whitespace already normalized')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
