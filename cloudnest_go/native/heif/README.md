# Optional native HEIF decoder

Build this separately from the Go server with a maintained **libheif >= 1.23.2** development package and a built-in HEVC decoder. The Go server remains free of CGO. The helper disables external codec plugin discovery. Native library security updates are the operator's responsibility; Go's vulnerability scanner does not scan libheif/libde265.

```sh
scripts/build-heif-helper.sh /absolute/new/cloudnest-heif
export IMAGE_HEIF_HELPER=/absolute/new/cloudnest-heif
```

The build script normally uses `pkg-config libheif`. For a specific installation, set `CLOUDNEST_HEIF_INCLUDE_DIR` (the directory containing `libheif/`) and `CLOUDNEST_HEIF_LIBRARY` (absolute library file). It embeds that library directory as an rpath. No library, prebuilt helper or downloaded third-party headers are committed. The application does not invoke a compiler or download a decoder at runtime.

The helper takes **no source path or rendering arguments**. A parent process supplies a verified regular file on FD 3 and a pipe on stdout. It opens `/dev/fd/3`, reads a maximum 64 MiB source, requires exactly one top-level primary image, applies native crop/mirror/rotation and scales to at most 1600 pixels per edge. Auxiliary images, multiple primary images and sequences are not claimed as fully indexed. Multi-image containers are rejected before pixel decoding.

Limits are set before parsing: 15 CPU seconds, 20-second native alarm, 64 FDs, no core or regular output files, and Linux 768 MiB RLIMIT_AS. Libheif retains its default security limits, further capped at 24M pixels, 4096 items/tiles/extents, 128 MiB per allocation and 512 MiB tracked total memory. The parent imposes a 15-second operation deadline, bounded stdout/stderr, process-group cancellation and a macOS RSS watchdog. The macOS watchdog can overshoot between observations and is **not** a hard RSS ceiling or filesystem/network sandbox. The parent reserves the shared image decode pool exclusively; the existing 512 MiB pool is not a claim about combined native/Go memory.

## Wire protocol v1

All integers are unsigned 32-bit big-endian. A successful stream contains exactly:

1. Eight bytes `CNHEIF01`.
2. Original transformed width, original transformed height, scaled width, scaled height, EXIF byte length, top-level image count, alpha flag (0 straight RGBA, 1 premultiplied RGBA).
3. EXIF TIFF bytes, at most 256 KiB, with the HEIF offset prefix already removed.
4. Scaled RGBA rows with no stride padding, exactly width × height × 4 bytes.

No text diagnostics or filename-derived data belong on stdout. The Go reader validates version, dimensions, exact scaling, lengths, flags, single-image count and TIFF bounds before creating a bounded PNG. EXIF dates/GPS are retained; EXIF orientation is not applied a second time after native HEIF geometric transforms. Failed/truncated/extra output is never a partial success.

Exit status: 20 invalid input/decode failure; 21 explicit resource bound; 22 unavailable decoder/runtime; 23 output failure; 24 unsupported multi-image input. Signals and other failures are rejected by the caller. Libheif errors may be classified as invalid input even when an internal library limit caused the rejection; the API must not infer successful extraction from that distinction.

## Validation

```sh
CLOUDNEST_TEST_HEIF_HELPER=/absolute/cloudnest-heif go -C cloudnest_go test -race -count=1 ./internal/imageprep
CLOUDNEST_TEST_HEIF_HELPER=/absolute/cloudnest-heif scripts/test-heif-model.sh
```

The latter uses real local Ollama and fresh disposable MySQL/Redis. It checks upload/index/semantic retrieval/manual OCR/thumbnail, plus missing-decoder failures. It is separate from the ordinary real-model suite and keeps that suite's six-minute limit unchanged. The backend CI compiles its `heifmodel` tag. The separate `heif-native` job builds both libraries and runs the native decoder on Linux using `scripts/test-heif-linux.sh`.

Native decoder tests passed on macOS arm64 and Alpine Linux arm64 with libheif 1.23.2 and libde265 1.1.1. They cover single-image HEIC, EXIF preservation and 2000×1000 → 1600×800 scaling. The end-to-end Ollama/API run covered macOS arm64. HDR/color fidelity, iPhone depth variants, malformed native corpus stress and multi-image handling still require broader acceptance.

API basis: [libheif v1.23.2](https://github.com/strukturag/libheif/tree/v1.23.2/libheif/api/libheif). This helper is original project code; libheif is dynamically linked under its upstream licensing terms.
