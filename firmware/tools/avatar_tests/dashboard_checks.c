#include "host.h"
#include <assert.h>
#include "ui/dashboard.c"

static unsigned active_page(void)
{
    return lv_obj_get_index(lv_tileview_get_tile_active(s_tiles));
}

void dashboard_checks(void)
{
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
