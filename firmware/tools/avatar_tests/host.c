// Real LVGL software renderer with transport, metrics and panel boundaries stubbed.
#include "host.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "ui/dashboard.h"
#include "metrics.h"
#include "net.h"

bool host_accept_commands = true, host_last_muted;
unsigned host_wakes, host_cancels, host_clears, host_mutes;
unsigned host_screen_begins, host_screen_finishes;
bool voice_request_wake(void) { ++host_wakes; return host_accept_commands; }
bool voice_request_cancel(void) { ++host_cancels; return host_accept_commands; }
bool voice_request_clear(void) { ++host_clears; return host_accept_commands; }
bool voice_request_mute(bool muted) { ++host_mutes; host_last_muted = muted; return host_accept_commands; }
void display_voice_begin(void) { ++host_screen_begins; }
void display_voice_finish(void) { ++host_screen_finishes; }
void metrics_snapshot(metrics_t *out) { memset(out, 0, sizeof(*out)); }
void net_get_status(net_status_t *out) { memset(out, 0, sizeof(*out)); out->state = NET_CONNECTED; }
// Quota/NVS is outside these tests; dashboard navigation and all voice widgets are real.
void quota_page_create(lv_obj_t *parent) { (void)parent; }
void quota_page_tick(void) {}

void host_advance(uint32_t ms)
{
    while (ms) {
        uint32_t step = ms < 10 ? ms : 10;
        lv_tick_inc(step);
        lv_timer_handler();
        ms -= step;
    }
}

void host_snapshot(const char *directory, const char *name, lv_obj_t *root)
{
    lv_obj_update_layout(root);
    lv_draw_buf_t *image = lv_snapshot_take(root, LV_COLOR_FORMAT_RGB888);
    assert(image && image->header.w == 480 && image->header.h == 480);
    // Rendering must include both Chinese text and the sprite. Metadata-only
    // glyph/frame assertions miss disabled font compression or image decoders.
    unsigned title_pixels = 0, avatar_pixels = 0;
    for (unsigned y = 15; y < 356; ++y) {
        const uint8_t *row = image->data + y * image->header.stride;
        for (unsigned x = 22; x < 384; ++x) {
            const uint8_t *pixel = row + x * 3;
            bool foreground = pixel[0] != 35 || pixel[1] != 25 || pixel[2] != 16;
            if (x < 100 && y < 40 && foreground) ++title_pixels;
            if (x >= 96 && y >= 44 && foreground) ++avatar_pixels;
        }
    }
    assert(title_pixels > 50 && avatar_pixels > 1000);
    char path[2048];
    snprintf(path, sizeof(path), "%s/%s.ppm", directory, name);
    FILE *out = fopen(path, "wb");
    assert(out);
    fprintf(out, "P6\n%u %u\n255\n", image->header.w, image->header.h);
    for (unsigned y = 0; y < image->header.h; ++y) {
        const uint8_t *row = image->data + y * image->header.stride;
        for (unsigned x = 0; x < image->header.w; ++x) {
            // LVGL RGB888 uses B,G,R byte order, like the actual panel pipeline.
            uint8_t rgb[] = {row[x * 3 + 2], row[x * 3 + 1], row[x * 3]};
            assert(fwrite(rgb, 1, 3, out) == 3);
        }
    }
    assert(fclose(out) == 0);
    lv_draw_buf_destroy(image);
}

static void flush(lv_display_t *display, const lv_area_t *area, uint8_t *pixels)
{
    (void)area; (void)pixels;
    lv_display_flush_ready(display);
}

int main(int argc, char **argv)
{
    lv_init();
    lv_display_t *display = lv_display_create(480, 480);
    assert(display);
    static uint8_t pixels[480 * 40 * 4];
    lv_display_set_color_format(display, LV_COLOR_FORMAT_XRGB8888);
    lv_display_set_buffers(display, pixels, NULL, sizeof(pixels), LV_DISPLAY_RENDER_MODE_PARTIAL);
    lv_display_set_flush_cb(display, flush);
    dashboard_create();
    host_advance(250);
    voice_checks(argc > 1 ? argv[1] : ".");
    dashboard_checks();
    puts("PASS: avatar states, waveform, controls, UTF-8, Chinese glyphs, dwell/navigation/wrap; snapshots saved");
    lv_deinit();
    return 0;
}
