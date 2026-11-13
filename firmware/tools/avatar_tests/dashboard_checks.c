#include "host.h"
#include <assert.h>
#include <stdlib.h>
#include "ui/dashboard.c"

static unsigned active_page(void)
{
    return lv_obj_get_index(lv_tileview_get_tile_active(s_tiles));
}

static void assert_corner(uint32_t color)
{
    host_advance(40);
    lv_draw_buf_t *image = lv_snapshot_take(lv_screen_active(), LV_COLOR_FORMAT_RGB888);
    assert(image);
    // The empty screen corner is visible on all three tiles, including quota's
    // stub tile. Widgets/avatar must stay above the shared background object.
    assert(image->data[0] == (color & 255));
    assert(image->data[1] == ((color >> 8) & 255));
    assert(image->data[2] == ((color >> 16) & 255));
    lv_draw_buf_destroy(image);
}
static void background_checks(void)
{
    assert(lv_obj_has_flag(s_background, LV_OBJ_FLAG_HIDDEN));
    assert_corner(C_BG);
    uint8_t *old = NULL;
    for (unsigned i = 0; i < 90; ++i) {
        uint16_t *pixels = malloc(480 * 480 * 2);
        assert(pixels);
        uint16_t raw = i % 3 == 0 ? 0xf800 : i % 3 == 1 ? 0x07e0 : 0x001f;
        uint32_t rgb = i % 3 == 0 ? 0xff0000 : i % 3 == 1 ? 0x00ff00 : 0x0000ff;
        for (unsigned n = 0; n < 480 * 480; ++n) pixels[n] = raw;
        assert(dashboard_set_background((uint8_t *)pixels, 1000));
        free(old); // production ownership: former source is safe immediately
        old = (uint8_t *)pixels;
        assert(dashboard_show_page((dashboard_page_t)(i % 3)));
        assert_corner(rgb);
        assert(lv_obj_get_index(s_background) < lv_obj_get_index(s_tiles));
        assert(lv_obj_get_index(s_background) < lv_obj_get_index(s_overlay));
        if (i % 9 == 0) {
            lv_obj_remove_flag(s_overlay, LV_OBJ_FLAG_HIDDEN);
            // Avoid the network tick hiding our simulated provisioning overlay.
            lv_draw_buf_t *overlay = lv_snapshot_take(lv_screen_active(), LV_COLOR_FORMAT_RGB888);
            assert(overlay && overlay->data[0] == 20 && overlay->data[1] == 15 && overlay->data[2] == 11);
            lv_draw_buf_destroy(overlay);
            lv_obj_add_flag(s_overlay, LV_OBJ_FLAG_HIDDEN);
            assert(dashboard_set_background(NULL, 1000));
            free(old); old = NULL;
            assert_corner(C_BG);
        }
    }
    assert(dashboard_set_background(NULL, 1000));
    free(old);
    assert(lv_image_get_src(s_background) == NULL);
    for (unsigned page = 0; page < 3; ++page) {
        assert(dashboard_show_page((dashboard_page_t)page));
        assert_corner(C_BG);
    }
}

void dashboard_checks(void)
{
    background_checks();
    assert(!dashboard_show_page((dashboard_page_t)99));
    assert(dashboard_show_page(DASHBOARD_PAGE_QUOTA));
    dashboard_voice_begin();
    assert(dashboard_voice_active() && active_page() == DASHBOARD_PAGE_VOICE);
    unsigned finishes = host_screen_finishes;
    dashboard_voice_finish();
    host_advance(2990);
    assert(dashboard_voice_active() && active_page() == DASHBOARD_PAGE_VOICE);
    dashboard_voice_finish(); // repeated idle must not extend the deadline
    host_advance(260);
    assert(!dashboard_voice_active() && active_page() == DASHBOARD_PAGE_QUOTA);
    assert(host_screen_finishes == finishes + 1);

    dashboard_voice_begin();
    dashboard_voice_finish();
    host_advance(1000);
    dashboard_voice_begin(); // fresh activity cancels pending restore
    host_advance(4000);
    assert(dashboard_voice_active() && active_page() == DASHBOARD_PAGE_VOICE);
    dashboard_voice_finish();
    host_advance(3250);
    assert(!dashboard_voice_active() && active_page() == DASHBOARD_PAGE_QUOTA);

    dashboard_voice_begin();
    dashboard_voice_finish();
    host_advance(1000);
    assert(dashboard_show_page(DASHBOARD_PAGE_METRICS)); // navigation during dwell
    host_advance(2500);
    assert(!dashboard_voice_active() && active_page() == DASHBOARD_PAGE_METRICS);

    dashboard_voice_begin();
    // Simulate tileview's swipe result and real navigation event.
    lv_tileview_set_tile_by_index(s_tiles, DASHBOARD_PAGE_QUOTA, 0, LV_ANIM_OFF);
    lv_obj_send_event(s_tiles, LV_EVENT_VALUE_CHANGED, NULL);
    dashboard_voice_finish();
    host_advance(3250);
    assert(!dashboard_voice_active() && active_page() == DASHBOARD_PAGE_QUOTA);

    // Deadline arithmetic must survive the uint32 LVGL millisecond tick wrapping.
    lv_tick_inc(UINT32_MAX - lv_tick_get() - 1000);
    dashboard_voice_begin();
    dashboard_voice_finish();
    host_advance(2990);
    assert(dashboard_voice_active());
    host_advance(260);
    assert(!dashboard_voice_active() && active_page() == DASHBOARD_PAGE_QUOTA);
}
