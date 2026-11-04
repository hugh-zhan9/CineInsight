#!/usr/bin/env python3
"""Bundle the JPEG coefficient-domain optimizer and its library, never install it globally."""
import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def require_supported_macos(path):
    output = run('/usr/bin/xcrun', 'vtool', '-show-build', str(path))
    versions = re.findall(r'\bminos\s+(\d+)\.(\d+)', output)
    if 'platform MACOS' not in output or not versions or any((int(a), int(b)) > (15, 0) for a, b in versions):
        raise ValueError('JPEG tools must target macOS 15 or earlier; rebuild with scripts/build_image_tools.sh')


def bundle(app, binary=None):
    app = Path(app).resolve()
    if not app.is_dir() or app.suffix != '.app':
        raise ValueError('Expected an existing build .app directory')
    resources = app / 'Contents' / 'Resources'
    if any(path.is_symlink() for path in (app / 'Contents', resources)):
        raise ValueError('Bundle resource directories must not be symlinks')
    source = Path(binary or Path(__file__).resolve().parent.parent / 'build/image-tools/bin/jpegtran').resolve()
    if not source.is_file() or not os.access(source, os.X_OK):
        raise ValueError('Build strict JPEG tools first: bash scripts/build_image_tools.sh')
    require_supported_macos(source)
    prefix = source.parent.parent
    libraries = []
    for line in run('/usr/bin/otool', '-L', str(source)).splitlines()[1:]:
        dependency = line.strip().split(' (', 1)[0]
        if dependency.startswith(('/usr/lib/', '/System/Library/')):
            continue
        if Path(dependency).name != 'libjpeg.8.dylib':
            raise ValueError('Unexpected jpegtran dependency: ' + dependency)
        library = prefix / 'lib' / 'libjpeg.8.dylib'
        if not library.is_file():
            raise ValueError('Matching libjpeg library is missing')
        require_supported_macos(library.resolve())
        libraries.append((dependency, library.resolve()))
    if len(libraries) != 1:
        raise ValueError('Expected one relocatable libjpeg dependency')
    docs = prefix / 'share' / 'doc' / 'libjpeg-turbo'
    licenses = [docs / name for name in ('LICENSE.md', 'README.ijg', 'README.md')]
    if not all(path.is_file() for path in licenses):
        raise ValueError('libjpeg-turbo redistribution notices are missing')
    resources.mkdir(parents=True, exist_ok=True)
    target = resources / 'image-tools'
    with tempfile.TemporaryDirectory(prefix='.image-tools-', dir=resources) as temporary:
        staged = Path(temporary) / 'image-tools'
        staged.mkdir()
        executable = staged / 'jpegtran'
        shutil.copyfile(source, executable)
        executable.chmod(0o755)
        for dependency, library in libraries:
            # Keep the ABI name used by the executable, without copying a symlink.
            copied = staged / 'libjpeg.8.dylib'
            shutil.copyfile(library, copied)
            copied.chmod(0o755)
            run('/usr/bin/install_name_tool', '-change', dependency, '@loader_path/libjpeg.8.dylib', str(executable))
            run('/usr/bin/install_name_tool', '-id', '@loader_path/libjpeg.8.dylib', str(copied))
            run('/usr/bin/codesign', '--force', '--sign', '-', str(copied))
        notice_dir = staged / 'licenses'
        notice_dir.mkdir()
        for license_path in licenses:
            shutil.copyfile(license_path, notice_dir / license_path.name)
        run('/usr/bin/codesign', '--force', '--sign', '-', str(executable))
        # install_name_tool invalidates the original signatures. The normal build
        # signs every Mach-O file again with the application's pinned identity.
        run(str(executable), '-version')
        if target.is_symlink():
            target.unlink()
        elif target.exists():
            shutil.rmtree(target)
        staged.rename(target)
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('app', type=Path)
    parser.add_argument('--jpegtran', type=Path)
    args = parser.parse_args()
    try:
        target = bundle(args.app, args.jpegtran)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, 'Image tool packaging failed: ' + str(error) + '\n')
    print('Bundled JPEG lossless optimizer: ' + str(target))


if __name__ == '__main__':
    main()
