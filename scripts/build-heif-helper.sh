#!/usr/bin/env bash
# Optional native helper; Go server builds do not acquire a CGO dependency.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
output=${1:?Usage: build-heif-helper.sh /absolute/new/cloudnest-heif}
[[ "$output" == /* && ! -e "$output" ]] || { echo 'Output must be a new absolute path.' >&2; exit 1; }
compiler=${CC:-cc}
if [[ -n "${CLOUDNEST_HEIF_INCLUDE_DIR:-}" && -n "${CLOUDNEST_HEIF_LIBRARY:-}" ]]; then
  [[ "$CLOUDNEST_HEIF_INCLUDE_DIR" == /* && "$CLOUDNEST_HEIF_LIBRARY" == /* ]] || exit 1
  "$compiler" -std=c11 -O2 -Wall -Wextra -Werror -I"$CLOUDNEST_HEIF_INCLUDE_DIR" \
    "$root/cloudnest_go/native/heif/main.c" "$CLOUDNEST_HEIF_LIBRARY" \
    -Wl,-rpath,"$(dirname "$CLOUDNEST_HEIF_LIBRARY")" -o "$output"
else
  pkg-config --atleast-version=1.23.2 libheif
  # The server launches the helper with a minimal environment. Record the
  # selected library directory for installations outside the loader defaults.
  libdir=$(pkg-config --variable=libdir libheif)
  [[ "$libdir" == /* ]] || { echo 'libheif libdir must be absolute.' >&2; exit 1; }
  # pkg-config emits compiler flags, not user document content.
  read -r -a flags <<< "$(pkg-config --cflags --libs libheif)"
  "$compiler" -std=c11 -O2 -Wall -Wextra -Werror \
    "$root/cloudnest_go/native/heif/main.c" "${flags[@]}" -Wl,-rpath,"$libdir" -o "$output"
fi
"$output" --version
