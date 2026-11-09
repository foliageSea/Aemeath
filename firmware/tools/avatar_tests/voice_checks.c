// Include the production unit to observe frame selection and widget geometry.
// No substitute animation, snapshot or LVGL implementation is used.
#include "host.h"
#include <assert.h>
#include <math.h>
#include <stdio.h>
#include "ui/dashboard.h"
#include "ui/voice_page.c"

static void click(int command)
{
    // The real buttons occupy the final four children of the voice tile.
    unsigned count = lv_obj_get_child_count(s_root);
    lv_obj_t *button = lv_obj_get_child(s_root, count - 4 + command);
    lv_obj_send_event(button, LV_EVENT_CLICKED, NULL);
}

void voice_checks(const char *directory)
{
    assert(dashboard_show_page(DASHBOARD_PAGE_VOICE));
    host_advance(40);
    assert(s_frame >= 0 && s_frame < avatar_sequences[AVATAR_IDLE].count);
    lv_font_glyph_dsc_t glyph;
    const char *chinese = "爱弥斯我在听正在识别让我想想准备说话遇到问题唤醒取消清空静音你好小";
    while (*chinese) {
        const unsigned char *utf8 = (const unsigned char *)chinese;
        assert((utf8[0] & 0xf0) == 0xe0); // these UI characters all use three UTF-8 bytes
        uint32_t cp = ((utf8[0] & 0x0f) << 12) | ((utf8[1] & 0x3f) << 6) | (utf8[2] & 0x3f);
        assert(lv_font_get_glyph_dsc(&voice_font_20, &glyph, cp, 0) && !glyph.is_placeholder);
        chinese += 3;
    }
    host_snapshot(directory, "voice-idle", s_root);

    voice_page_update("wake", "你好，我在这里", 1);
    voice_page_tick();
    assert(s_waving && s_sequence == AVATAR_WAVING);
    host_snapshot(directory, "voice-wake", s_root);
    voice_page_update("listening", NULL, 1);
    voice_page_tick();
    host_advance(560);
    voice_page_tick();
    assert(!s_waving && s_sequence == AVATAR_WAITING);
    voice_page_level(1.0f);
    voice_page_tick();
    host_advance(1); // allow layout to resolve the bar heights
    bool tall = false;
    for (int i = 0; i < WAVE_BARS; ++i) {
        assert(!lv_obj_has_flag(s_bars[i], LV_OBJ_FLAG_HIDDEN));
        tall |= lv_obj_get_height(s_bars[i]) > 2;
    }
    assert(tall);
    host_snapshot(directory, "voice-listening", s_root);
    voice_page_level(NAN);
    voice_page_level(-1);
    host_advance(1000);
    assert(s_level < 0.001f);

    voice_page_update("thinking", "今天想聊些什么？", 1);
    voice_page_tick();
    assert(s_sequence == AVATAR_REVIEW);
    assert(lv_obj_has_flag(s_bars[0], LV_OBJ_FLAG_HIDDEN));
    host_snapshot(directory, "voice-thinking", s_root);
    voice_page_update("speaking", "你好，我是爱弥斯。很高兴见到你！", 1);
    voice_page_level(0.8f);
    voice_page_tick();
    assert(s_sequence == AVATAR_RUNNING && !lv_obj_has_flag(s_bars[0], LV_OBJ_FLAG_HIDDEN));
    host_snapshot(directory, "voice-speaking", s_root);

    voice_page_update("error", "连接中断，请稍后重试", 1);
    voice_page_tick();
    const avatar_sequence_t *failed = &avatar_sequences[AVATAR_FAILED];
    assert(s_frame == failed->first);
    for (int i = 1; i < failed->count; ++i) {
        host_advance(failed->frame_ms);
        voice_page_tick();
        assert(s_frame == failed->first + i);
    }
    host_advance(6000);
    assert(s_frame == failed->first + failed->count - 1);
    host_snapshot(directory, "voice-error", s_root);

    // Even when transport wake/listening coalesce into one UI snapshot, greet once.
    voice_page_update("wake", "你好", 2);
    voice_page_update("listening", NULL, 2);
    voice_page_tick();
    assert(s_waving && s_sequence == AVATAR_WAVING);
    voice_page_update("idle", "", 2);
    voice_page_tick();
    assert(!s_waving);
    click(0); click(1); click(2); click(3);
    assert(host_wakes == 1 && host_cancels == 1 && host_clears == 1);
    assert(host_mutes == 1 && host_last_muted);
    voice_page_tick();
    assert(!strcmp(lv_label_get_text(s_mute_label), "取消静音"));
    // Reconnection reset is a pending snapshot, and mutates LVGL only on tick.
    voice_page_set_muted(false);
    assert(!strcmp(lv_label_get_text(s_mute_label), "取消静音"));
    voice_page_tick();
    assert(!s_muted && !strcmp(lv_label_get_text(s_mute_label), "静音"));
    host_accept_commands = false;
    click(3);
    assert(!s_muted);
    assert(!strcmp(lv_label_get_text(s_text), "语音尚未连接，请检查桌面端"));
    host_accept_commands = true;

    char long_text[1300];
    memset(long_text, 'a', 1022);
    memcpy(long_text + 1022, "你好", 7);
    voice_page_update("listening", long_text, 2);
    assert(strlen(s_pending.text) == 1022); // truncate before split UTF-8 character
    voice_page_tick();
    voice_page_update("idle", "", 2);
    voice_page_tick();
}
