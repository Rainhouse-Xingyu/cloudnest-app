#!/usr/bin/env bash
# Build the two native libraries in a private prefix, then run the Go/native
# descriptor, pixel, EXIF and scaling tests on Linux. No service DB is used.
set -euo pipefail
[[ $(uname -s) == Linux ]] || { echo 'Run on Linux.' >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
prefix="$work/install"
export PKG_CONFIG_PATH="$prefix/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"

build_library() {
  local name=$1 revision=$2 checksum=$3
  shift 3
  local archive="$work/$name.tar.gz" source="$work/$name"
  if [[ -n "${CLOUDNEST_HEIF_ARCHIVE_DIR:-}" ]]; then
    archive="$CLOUDNEST_HEIF_ARCHIVE_DIR/$name-$revision.tar.gz"
  else
    curl --fail --location --silent --show-error --retry 2 --max-time 180 \
      "https://codeload.github.com/strukturag/$name/tar.gz/$revision" --output "$archive"
  fi
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c
  mkdir "$source"
  tar -xzf "$archive" -C "$source" --strip-components=1
  cmake -S "$source" -B "$source/build" -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_PREFIX_PATH="$prefix" -DCMAKE_INSTALL_PREFIX="$prefix" -DCMAKE_INSTALL_LIBDIR=lib \
    -DCMAKE_INSTALL_RPATH="$prefix/lib" "$@"
  cmake --build "$source/build" --parallel 2
  cmake --install "$source/build"
}

build_library libde265 4dd701fffac01632ffd5cabc5ef10deb56accba1 \
  43b3629cfa11a6e12be4d1fe7b7857eeeb17c84ce113f87fa93b6ab7478db55b \
  -DENABLE_DECODER=OFF -DENABLE_SDL=OFF
build_library libheif ac1cb05c39008f01525c991ff8b88f84ddf70fd2 \
  36c9b259386f1ffb3ed12d7ca01664fde8ef03802ded750de17db53303b099e7 \
  -DENABLE_PLUGIN_LOADING=OFF -DWITH_LIBDE265=ON -DWITH_LIBDE265_PLUGIN=OFF \
  -DWITH_X265=OFF -DWITH_X264=OFF -DWITH_AOM_DECODER=OFF -DWITH_AOM_ENCODER=OFF \
  -DWITH_OpenH264_DECODER=OFF -DWITH_EXAMPLES=OFF -DBUILD_TESTING=OFF -DWITH_GDK_PIXBUF=OFF

export CLOUDNEST_TEST_HEIF_HELPER="$work/cloudnest-heif"
"$root/scripts/build-heif-helper.sh" "$CLOUDNEST_TEST_HEIF_HELPER"
printf 'Linux %s, libde265 %s, libheif %s\n' "$(uname -m)" \
  "$(pkg-config --modversion libde265)" "$(pkg-config --modversion libheif)"
if [[ -n "${CLOUDNEST_HEIF_TEST_BINARY:-}" ]]; then
  # The macOS developer workflow supplies a Go test binary cross-compiled for
  # this Linux architecture. The actual Go and native processes run here.
  cd "$root/cloudnest_go/internal/imageprep"
  "$CLOUDNEST_HEIF_TEST_BINARY" -test.v -test.run '^TestHEIFNativeOptIn$'
else
  go -C "$root/cloudnest_go" test -race -count=1 -v -run '^TestHEIFNativeOptIn$' ./internal/imageprep
fi
