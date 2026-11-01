/* CloudNest's isolated primary-image decoder. No parser runs inside Go.
 * Protocol is documented in README.md. Link to a maintained system libheif;
 * neither third-party source nor binary libraries are bundled here. */
#define _POSIX_C_SOURCE 200809L
#include <libheif/heif.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <unistd.h>

_Static_assert(sizeof(size_t) >= 8, "CloudNest native image decoding requires a 64-bit target");

#define REQUIRED_VERSION 0x01170200u /* 1.23.2 */
#define MAX_PIXELS 24000000u
#define MAX_SIDE 16384
#define MAX_EDGE 1600
#define MAX_SOURCE (64u << 20)
#define MAX_EXIF (256u << 10)
#define MEMORY_LIMIT (768u << 20)
enum { INVALID = 20, LIMIT = 21, UNAVAILABLE = 22, OUTPUT = 23, UNSUPPORTED = 24 };

static int limit_resource(int resource, rlim_t cap) {
    struct rlimit r;
    if (getrlimit(resource, &r)) return 0;
    if (r.rlim_max < cap) cap = r.rlim_max;
    r.rlim_cur = r.rlim_max = cap;
    return setrlimit(resource, &r) == 0;
}

static int dimensions(int w, int h) {
    return w > 0 && h > 0 && w <= MAX_SIDE && h <= MAX_SIDE &&
        (uint64_t)w * (uint64_t)h <= MAX_PIXELS;
}

static uint32_t get32(const uint8_t *p) {
    return (uint32_t)p[0] << 24 | (uint32_t)p[1] << 16 |
        (uint32_t)p[2] << 8 | p[3];
}

static int put32(uint32_t n) {
    uint8_t b[4] = {(uint8_t)(n >> 24), (uint8_t)(n >> 16),
                    (uint8_t)(n >> 8), (uint8_t)n};
    return fwrite(b, 1, sizeof b, stdout) == sizeof b;
}

int main(int argc, char **argv) {
    if (heif_get_version_number() < REQUIRED_VERSION) return UNAVAILABLE;
    if (argc == 2 && strcmp(argv[1], "--version") == 0) {
        printf("cloudnest-heif 1 libheif %s\n", heif_get_version());
        return 0;
    }
    if (argc != 1) return INVALID;
    struct stat source, output;
    if (fstat(3, &source) || !S_ISREG(source.st_mode) || source.st_size < 12) return INVALID;
    if (source.st_size > MAX_SOURCE) return LIMIT;
    if (fstat(STDOUT_FILENO, &output) || !S_ISFIFO(output.st_mode)) return OUTPUT;
    if (!limit_resource(RLIMIT_CPU, 15) || !limit_resource(RLIMIT_NOFILE, 64) ||
        !limit_resource(RLIMIT_CORE, 0) || !limit_resource(RLIMIT_FSIZE, 0)) return UNAVAILABLE;
#ifdef __linux__
    if (!limit_resource(RLIMIT_AS, MEMORY_LIMIT)) return UNAVAILABLE;
#endif
    alarm(20); /* Go must additionally cancel/kill and monitor RSS on macOS. */
    if (setenv("LIBHEIF_PLUGIN_PATH", "/nonexistent", 1)) return UNAVAILABLE;
    if (heif_init(NULL).code) return UNAVAILABLE;
    int result = INVALID;
    heif_context *ctx = heif_context_alloc();
    heif_image_handle *handle = NULL;
    heif_image *image = NULL, *scaled = NULL;
    heif_decoding_options *options = NULL;
    uint8_t *metadata = NULL;
    if (!heif_have_decoder_for_format(heif_compression_HEVC)) { result = UNAVAILABLE; goto done; }
    if (!ctx) { result = LIMIT; goto done; }
    /* Tighten existing defaults, never replace them with disabled limits. */
    heif_security_limits *limits = heif_context_get_security_limits(ctx);
    if (!limits || limits->version < 4) { result = UNAVAILABLE; goto done; }
#define TIGHTEN(field, cap) do { if (!limits->field || limits->field > (cap)) limits->field = (cap); } while (0)
    TIGHTEN(max_image_size_pixels, MAX_PIXELS);
    TIGHTEN(max_number_of_tiles, 4096);
    TIGHTEN(max_items, 4096);
    TIGHTEN(max_color_profile_size, MAX_EXIF);
    TIGHTEN(max_memory_block_size, 128u << 20);
    TIGHTEN(max_total_memory, 512u << 20);
    TIGHTEN(max_iloc_extents_per_item, 4096);
    TIGHTEN(max_children_per_box, 4096);
    TIGHTEN(max_sequence_frames, 1);
    TIGHTEN(max_number_of_file_brands, 64);
#undef TIGHTEN
    heif_context_set_max_decoding_threads(ctx, 0);
    if (heif_context_read_from_file(ctx, "/dev/fd/3", NULL).code) goto done;
    int count = heif_context_get_number_of_top_level_images(ctx);
    if (count < 1 || count > 4096) goto done;
    if (count != 1) { result = UNSUPPORTED; goto done; }
    if (heif_context_get_primary_image_handle(ctx, &handle).code) goto done;
    int source_w = heif_image_handle_get_width(handle);
    int source_h = heif_image_handle_get_height(handle);
    if (!dimensions(source_w, source_h) ||
        !dimensions(heif_image_handle_get_ispe_width(handle), heif_image_handle_get_ispe_height(handle))) {
        result = LIMIT; goto done;
    }
    /* Extract only the primary image's sole EXIF block, before pixel decode. */
    int n = heif_image_handle_get_number_of_metadata_blocks(handle, "Exif");
    size_t metadata_size = 0, offset = 0;
    if (n < 0 || n > 1) goto done;
    if (n == 1) {
        heif_item_id id;
        if (heif_image_handle_get_list_of_metadata_block_IDs(handle, "Exif", &id, 1) != 1) goto done;
        metadata_size = heif_image_handle_get_metadata_size(handle, id);
        if (metadata_size > MAX_EXIF + 4) { result = LIMIT; goto done; }
        if (metadata_size < 4) goto done;
        metadata = malloc(metadata_size);
        if (!metadata) { result = LIMIT; goto done; }
        if (heif_image_handle_get_metadata(handle, id, metadata).code) goto done;
        offset = (size_t)get32(metadata) + 4;
        if (offset >= metadata_size) goto done;
    }
    options = heif_decoding_options_alloc();
    if (!options || options->version < 8) { result = UNAVAILABLE; goto done; }
    options->strict_decoding = 1;
    options->ignore_transformations = 0;
    options->convert_hdr_to_8bit = 1;
    options->num_codec_threads = 1;
    if (heif_decode_image(handle, &image, heif_colorspace_RGB, heif_chroma_interleaved_RGBA, options).code) goto done;
    int w = heif_image_get_width(image, heif_channel_interleaved);
    int h = heif_image_get_height(image, heif_channel_interleaved);
    if (!dimensions(w, h) || w != source_w || h != source_h) goto done;
    if (w > MAX_EDGE || h > MAX_EDGE) {
        if (w >= h) { h = (int)((int64_t)h * MAX_EDGE / w); w = MAX_EDGE; }
        else { w = (int)((int64_t)w * MAX_EDGE / h); h = MAX_EDGE; }
        if (w < 1) w = 1;
        if (h < 1) h = 1;
        if (heif_image_scale_image(image, &scaled, w, h, NULL).code) goto done;
        heif_image_release(image); image = scaled; scaled = NULL;
    }
    /* Copy using the decoded channel's dimensions/stride, never item metadata. */
    w = heif_image_get_width(image, heif_channel_interleaved);
    h = heif_image_get_height(image, heif_channel_interleaved);
    size_t stride;
    const uint8_t *pixels = heif_image_get_plane_readonly2(image, heif_channel_interleaved, &stride);
    if (!pixels || w < 1 || h < 1 || w > MAX_EDGE || h > MAX_EDGE ||
        stride < (size_t)w * 4 || stride > MEMORY_LIMIT / (size_t)h) goto done;
    uint32_t exif_size = (uint32_t)(metadata_size - offset);
    int premultiplied = heif_image_is_premultiplied_alpha(image) != 0;
    result = OUTPUT;
    if (fwrite("CNHEIF01", 1, 8, stdout) != 8 || !put32((uint32_t)source_w) ||
        !put32((uint32_t)source_h) || !put32((uint32_t)w) || !put32((uint32_t)h) ||
        !put32(exif_size) || !put32((uint32_t)count) || !put32((uint32_t)premultiplied)) goto done;
    if (exif_size && fwrite(metadata + offset, 1, exif_size, stdout) != exif_size) goto done;
    for (int y = 0; y < h; y++) {
        size_t row = (size_t)w * 4;
        if (fwrite(pixels + (size_t)y * stride, 1, row, stdout) != row) goto done;
    }
    if (fflush(stdout)) goto done;
    result = 0;
done:
    free(metadata);
    if (options) heif_decoding_options_free(options);
    if (image) heif_image_release(image);
    if (scaled) heif_image_release(scaled);
    if (handle) heif_image_handle_release(handle);
    if (ctx) heif_context_free(ctx);
    heif_deinit();
    return result;
}
