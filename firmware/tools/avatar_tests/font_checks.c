// Exercise the production font decoder with distinct worker-local output buffers.
// LVGL 9.6 compressed RLE state is global; the font must not enter that path.
#include "lvgl.h"
#include <assert.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

LV_FONT_DECLARE(voice_font_20);
#define WORKERS 4
#define ROUNDS 2000
#define DIM 32
static const uint32_t letters[] = {
    0x7231, 0x5f25, 0x65af, 0x4f60, 0x597d, 0x6211, 0x542c,
    0x8bed, 0x97f3, 0x9192, 0x4e2d, 0x6587, 0x9f99, 0x9fa0, 0xffe5
};
#define LETTERS (sizeof(letters) / sizeof(letters[0]))
static uint64_t expected[LETTERS];
typedef struct {
    lv_draw_buf_t buffer;
    _Alignas(64) uint8_t pixels[DIM * DIM];
    unsigned index;
} worker_t;

static uint64_t glyph_hash(worker_t *worker, unsigned index)
{
    lv_font_glyph_dsc_t glyph = {0};
    assert(lv_font_get_glyph_dsc(&voice_font_20, &glyph, letters[index], 0));
    assert(!glyph.is_placeholder && glyph.box_w && glyph.box_h);
    assert(glyph.box_w <= DIM && glyph.box_h <= DIM);
    memset(worker->pixels, 0, sizeof(worker->pixels));
    const lv_draw_buf_t *result = voice_font_20.get_glyph_bitmap(&glyph, &worker->buffer);
    assert(result == &worker->buffer);
    uint32_t stride = lv_draw_buf_width_to_stride(glyph.box_w, LV_COLOR_FORMAT_A8);
    uint64_t hash = UINT64_C(14695981039346656037);
    unsigned visible = 0;
    for (unsigned y = 0; y < glyph.box_h; ++y) {
        for (unsigned x = 0; x < glyph.box_w; ++x) {
            uint8_t alpha = worker->pixels[y * stride + x];
            assert(alpha % 17 == 0); // unchanged 4bpp antialiasing, expanded to A8
            visible += alpha != 0;
            hash = (hash ^ alpha) * UINT64_C(1099511628211);
        }
    }
    assert(visible > 0);
    return hash;
}

static void *decode_worker(void *arg)
{
    worker_t *worker = arg;
    for (unsigned round = 0; round < ROUNDS; ++round) {
        for (unsigned i = 0; i < LETTERS; ++i) {
            unsigned index = (i + worker->index + round) % LETTERS;
            assert(glyph_hash(worker, index) == expected[index]);
        }
    }
    return NULL;
}

int main(void)
{
    lv_init();
    const lv_font_fmt_txt_dsc_t *font = voice_font_20.dsc;
    assert(font->bitmap_format == LV_FONT_FMT_TXT_PLAIN && font->bpp == 4);
    assert(LV_FONT_FMT_TXT_LARGE);
    lv_font_glyph_dsc_t high = {0};
    assert(lv_font_get_glyph_dsc(&voice_font_20, &high, 0xffe5, 0));
    assert(font->glyph_dsc[high.gid.index].bitmap_index > (1u << 20));
    worker_t workers[WORKERS];
    pthread_t threads[WORKERS];
    for (unsigned i = 0; i < WORKERS; ++i) {
        workers[i].index = i;
        assert(lv_draw_buf_init(&workers[i].buffer, DIM, DIM, LV_COLOR_FORMAT_A8, DIM,
                               workers[i].pixels, sizeof(workers[i].pixels)) == LV_RESULT_OK);
    }
    // Establish serial golden bitmaps before any competing reader exists.
    for (unsigned i = 0; i < LETTERS; ++i) expected[i] = glyph_hash(&workers[0], i);
    for (unsigned i = 0; i < WORKERS; ++i) assert(!pthread_create(&threads[i], NULL, decode_worker, &workers[i]));
    for (unsigned i = 0; i < WORKERS; ++i) assert(!pthread_join(threads[i], NULL));
    printf("PASS: plain 4bpp glyphs, >1MiB offsets, %u workers, %zu identical bitmap decodes\n",
           WORKERS, (size_t)WORKERS * ROUNDS * LETTERS);
    lv_deinit();
    return 0;
}
