#!/usr/bin/env python3
"""Make the macOS ncnn runtime load its own MoltenVK directly, without a system ICD."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

RUNTIME_VERSION = 'realesrgan-ncnn-vulkan-v0.2.0-cineinsight2'


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def bundle(runtime, library=None):
    runtime = Path(runtime).resolve()
    binary = runtime / 'bin/realesrgan-ncnn-vulkan'
    if not binary.is_file() or any(path.is_symlink() for path in runtime.rglob('*')):
        raise ValueError('Expected a runtime with regular files and an ncnn executable')
    source = Path(library or '/opt/homebrew/lib/libMoltenVK.dylib').resolve()
    if not source.is_file():
        raise ValueError('MoltenVK dynamic library is required')
    prefix = source.parent.parent
    if not (prefix / 'LICENSE').is_file() or not (prefix / 'README.md').is_file():
        raise ValueError('MoltenVK redistribution notices are missing')
    dependencies = [line.strip().split(' (', 1)[0] for line in run('/usr/bin/otool', '-L', str(binary)).splitlines()[1:]]
    gpu = [item for item in dependencies if 'libvulkan' in Path(item).name or 'libMoltenVK' in Path(item).name]
    if len(gpu) != 1:
        raise ValueError('Expected one Vulkan loader or MoltenVK dependency')
    unexpected = [item for item in dependencies if item not in gpu and not item.startswith(('/usr/lib/', '/System/Library/'))]
    if unexpected:
        raise ValueError('Unexpected non-system sidecar dependency: ' + ', '.join(unexpected))
    for line in run('/usr/bin/otool', '-L', str(source)).splitlines()[2:]:
        dependency = line.strip().split(' (', 1)[0]
        if not dependency.startswith(('/usr/lib/', '/System/Library/')):
            raise ValueError('MoltenVK has an unbundled dependency: ' + dependency)
    with tempfile.TemporaryDirectory(prefix='.enhance-driver-', dir=runtime.parent) as temporary:
        staged = Path(temporary) / 'runtime'
        shutil.copytree(runtime, staged)
        target_library = staged / 'lib/libMoltenVK.dylib'
        target_library.parent.mkdir(exist_ok=True)
        shutil.copyfile(source, target_library)
        target_library.chmod(0o755)
        target_binary = staged / 'bin/realesrgan-ncnn-vulkan'
        run('/usr/bin/install_name_tool', '-change', gpu[0], '@loader_path/../lib/libMoltenVK.dylib', str(target_binary))
        run('/usr/bin/install_name_tool', '-id', '@loader_path/libMoltenVK.dylib', str(target_library))
        for path in (target_library, target_binary):
            run('/usr/bin/codesign', '--force', '--sign', '-', str(path))
        notices = staged / 'licenses'
        notices.mkdir(exist_ok=True)
        shutil.copyfile(prefix / 'LICENSE', notices / 'MoltenVK-LICENSE.txt')
        shutil.copyfile(prefix / 'README.md', notices / 'MoltenVK-README.md')
        manifest_path = staged / 'manifest.json'
        if manifest_path.exists():
            manifest = json.loads(manifest_path.read_text())
            if manifest.get('runtime_version') not in ('realesrgan-ncnn-vulkan-v0.2.0-cineinsight1', RUNTIME_VERSION):
                raise ValueError('Unexpected runtime identity')
            manifest['runtime_version'] = RUNTIME_VERSION
            manifest['files'] = [
                {'path': str(path.relative_to(staged)), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}
                for path in sorted(staged.rglob('*'))
                if path.is_file() and path.name != 'manifest.json' and 'models' not in path.relative_to(staged).parts
            ]
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
        # Publish only a complete replacement; a failure before this point keeps
        # the old generated runtime untouched, including its model declarations.
        old = Path(temporary) / 'previous'
        runtime.rename(old)
        try:
            staged.rename(runtime)
        except OSError:
            old.rename(runtime)
            raise
    return runtime


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('runtime', type=Path)
    parser.add_argument('--moltenvk', type=Path)
    args = parser.parse_args()
    try:
        print('Bundled direct MoltenVK runtime: ' + str(bundle(args.runtime, args.moltenvk)))
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, 'GPU runtime packaging failed: ' + str(error) + '\n')


if __name__ == '__main__':
    main()
