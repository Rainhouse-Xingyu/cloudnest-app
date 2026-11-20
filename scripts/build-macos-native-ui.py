#!/usr/bin/env python3
"""Build the real workspace UI with a dedicated pinned-TLS fixture transport."""
from pathlib import Path
import argparse
import platform
import plistlib
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]


def build():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('manifest', nargs='?', type=Path)
    arguments = parser.parse_args()
    manifest = arguments.manifest.resolve(strict=True) if arguments.manifest else None
    if platform.system() != 'Darwin':
        raise RuntimeError('Native UI acceptance requires macOS')
    directory = Path(tempfile.mkdtemp(prefix='cloudnest-native-ui-build-', dir='/tmp'))
    app = directory / 'CloudNest Native UI.app'
    executable = app / 'Contents/MacOS/CloudNestNativeUI'
    executable.parent.mkdir(parents=True)
    # Copy the shipping view definitions verbatim, excluding the shipping @main
    # entry and its production transport. All remaining App/Core files are used.
    source = (ROOT / 'cloudnest_macos/App/CloudNestApp.swift').read_text()
    workspace = directory / 'WorkspaceWindow.swift'
    workspace.write_text('import SwiftUI\n\n' + source[source.index('private enum Page:'):])
    sources = sorted((ROOT / 'cloudnest_macos/Core').glob('*.swift'))
    sources += sorted(p for p in (ROOT / 'cloudnest_macos/App').glob('*.swift') if p.name != 'CloudNestApp.swift')
    sources += [workspace, ROOT / 'cloudnest_macos/NativeUITestSupport/CloudNestNativeUI.swift']
    subprocess.run(['xcrun', 'swiftc', '-parse-as-library', '-O', '-swift-version', '6',
                    '-target', platform.machine() + '-apple-macos26.0',
                    '-module-name', 'CloudNestNativeUI', *map(str, sources), '-o', str(executable)], check=True)
    info = dict(CFBundleExecutable='CloudNestNativeUI', CFBundleIdentifier='org.cloudnest.macos.native-ui-test.' + uuid.uuid4().hex,
                          CFBundleName='CloudNest Native UI', CFBundlePackageType='APPL',
                          CFBundleShortVersionString='0.1.0', CFBundleVersion='1', LSMinimumSystemVersion='26.0',
                          NSHighResolutionCapable=True)
    if manifest is not None:
        info['CloudNestNativeUIManifest'] = str(manifest)
    with (app / 'Contents/Info.plist').open('wb') as file:
        plistlib.dump(info, file)
    subprocess.run(['codesign', '--force', '--sign', '-', str(app)], check=True)
    subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True)
    print(app)


if __name__ == '__main__':
    build()
