// Voice companion. Flash-backed I8 frames; LVGL work stays on the UI task.
#include "voice_page.h"

#include <math.h>
#include <stdbool.h>
#include <string.h>
#include "freertos/FreeRTOS.h"
#include "assets/avatar_frames.h"
#include "voice/voice.h"
// LVGL 9.5 keeps cache controls outside the umbrella public header.
#include "src/misc/cache/instance/lv_image_cache.h"
#include "src/misc/cache/instance/lv_image_header_cache.h"

LV_FONT_DECLARE(voice_font_20);
#define TEXT_BYTES 1024
#define WAVE_BARS 12
#define C_TEXT 0xF3EDF7
#define C_DIM 0xB3ABBE
#define C_ACCENT 0xEBAAC8
#define C_ERROR 0xFF938F

typedef enum {
    PH_IDLE, PH_WAKE, PH_LISTENING, PH_RECOGNIZING, PH_THINKING,
    PH_SYNTHESIZING, PH_SPEAKING, PH_ERROR
} phase_t;

typedef struct {
    phase_t phase;
    uint32_t turn, revision, wake_serial;
    char text[TEXT_BYTES];
    float level;
    bool muted;
} snapshot_t;

static portMUX_TYPE s_lock = portMUX_INITIALIZER_UNLOCKED;
static snapshot_t s_pending = {.revision = 1};
static lv_obj_t *s_root, *s_image, *s_status, *s_text, *s_mute_label;
static lv_obj_t *s_bars[WAVE_BARS];
static uint32_t s_seen, s_wake_seen, s_wave_start, s_sequence_start;
static phase_t s_phase;
static avatar_sequence_id_t s_sequence = AVATAR_IDLE;
static bool s_waving, s_muted;
static int s_frame = -1;
static float s_level;
// LV_LABEL_LONG_DOT may temporarily replace the label buffer tail with dots.
// Keep the original caption separately so duplicate snapshots do not relayout it.
static char s_caption[TEXT_BYTES];

static phase_t parse_phase(const char *state)
{
    static const char *const names[] = {"idle", "wake", "listening", "recognizing",
        "thinking", "synthesizing", "speaking", "error"};
    if (state) for (int i = 0; i < 8; ++i) if (!strcmp(state, names[i])) return (phase_t)i;
    return PH_IDLE;
}

static void copy_text(char *dst, const char *src)
{
    size_t n = strnlen(src, TEXT_BYTES - 1);
    // Truncate only at a UTF-8 boundary so LVGL never sees a partial character.
    while (n && (((unsigned char)src[n] & 0xc0) == 0x80)) --n;
    memcpy(dst, src, n);
    dst[n] = 0;
}

void voice_page_update(const char *state, const char *text, uint32_t turn)
{
    phase_t phase = parse_phase(state);
    char caption[TEXT_BYTES];
    if (text) copy_text(caption, text);
    portENTER_CRITICAL(&s_lock);
    if (phase == PH_WAKE && (s_pending.phase != PH_WAKE || s_pending.turn != turn))
        ++s_pending.wake_serial;
    s_pending.phase = phase;
    s_pending.turn = turn;
    if (text) memcpy(s_pending.text, caption, strlen(caption) + 1);
    ++s_pending.revision;
    portEXIT_CRITICAL(&s_lock);
}

void voice_page_level(float level)
{
    if (!isfinite(level) || level < 0) level = 0;
    if (level > 1) level = 1;
    portENTER_CRITICAL(&s_lock);
    if (level > s_pending.level) s_pending.level = level;
    portEXIT_CRITICAL(&s_lock);
}

void voice_page_set_muted(bool muted)
{
    portENTER_CRITICAL(&s_lock);
    s_pending.muted = muted;
    ++s_pending.revision;
    portEXIT_CRITICAL(&s_lock);
}

static avatar_sequence_id_t sequence_for(phase_t phase)
{
    switch (phase) {
    case PH_WAKE: case PH_LISTENING: return AVATAR_WAITING;
    case PH_RECOGNIZING: case PH_THINKING: return AVATAR_REVIEW;
    case PH_SYNTHESIZING: case PH_SPEAKING: return AVATAR_RUNNING;
    case PH_ERROR: return AVATAR_FAILED;
    default: return AVATAR_IDLE;
    }
}

static const char *status_for(phase_t phase)
{
    static const char *const text[] = {"等你呼唤", "我在这里", "我在听", "正在识别",
        "让我想想", "正在准备", "正在说话", "遇到问题"};
    return text[phase];
}

static void set_short_label(lv_obj_t *obj, const char *text)
{
    if (strcmp(lv_label_get_text(obj), text)) lv_label_set_text(obj, text);
}

static void set_caption(const char *text)
{
    if (!strcmp(s_caption, text)) return;
    copy_text(s_caption, text);
    lv_label_set_text(s_text, s_caption);
}

static void action(lv_event_t *e)
{
    intptr_t command = (intptr_t)lv_event_get_user_data(e);
    bool accepted = false;
    switch (command) {
    case 0: accepted = voice_request_wake(); break;
    case 1: accepted = voice_request_cancel(); break;
    case 2: accepted = voice_request_clear(); break;
    case 3:
        accepted = voice_request_mute(!s_muted);
        if (accepted) {
            s_muted = !s_muted;
            voice_page_set_muted(s_muted);
            set_short_label(s_mute_label, s_muted ? "取消静音" : "静音");
        }
        break;
    }
    if (!accepted) set_caption("语音尚未连接，请检查桌面端");
    else if (command == 2) set_caption("对话已清空");
}

static lv_obj_t *label(lv_obj_t *parent, const char *text, uint32_t color)
{
    lv_obj_t *obj = lv_label_create(parent);
    lv_obj_set_style_text_font(obj, &voice_font_20, 0);
    lv_obj_set_style_text_color(obj, lv_color_hex(color), 0);
    lv_label_set_text(obj, text);
    return obj;
}

static void timer_cb(lv_timer_t *timer)
{
    (void)timer;
    voice_page_tick();
}

void voice_page_create(lv_obj_t *parent)
{
    s_root = parent;
    lv_obj_set_style_bg_color(parent, lv_color_hex(0x101923), 0);
    lv_obj_set_style_bg_opa(parent, LV_OPA_COVER, 0);
    lv_obj_set_style_pad_all(parent, 0, 0);
    // Two native-size ARGB decoded frames plus metadata, never the whole atlas.
    // A new decode can temporarily coexist with the two cached frames.
    lv_image_cache_resize(2 * AVATAR_WIDTH * AVATAR_HEIGHT * 4 + 4096, true);
    lv_image_header_cache_resize(8, true);

    lv_obj_t *title = label(parent, "爱弥斯", C_TEXT);
    lv_obj_set_pos(title, 22, 15);
    s_status = label(parent, "等你呼唤", C_ACCENT);
    lv_obj_align(s_status, LV_ALIGN_TOP_RIGHT, -22, 15);

    s_image = lv_image_create(parent);
    lv_image_set_src(s_image, &avatar_frames[0]);
    lv_image_set_pivot(s_image, 0, 0);
    lv_image_set_scale(s_image, 384); // 192x208 -> centered 288x312
    lv_image_set_antialias(s_image, false);
    lv_obj_set_pos(s_image, 96, 44);
    lv_obj_remove_flag(s_image, LV_OBJ_FLAG_CLICKABLE);

    for (int i = 0; i < WAVE_BARS; ++i) {
        s_bars[i] = lv_obj_create(parent);
        lv_obj_remove_style_all(s_bars[i]);
        lv_obj_set_style_bg_color(s_bars[i], lv_color_hex(C_ACCENT), 0);
        lv_obj_set_style_bg_opa(s_bars[i], LV_OPA_COVER, 0);
        lv_obj_set_style_radius(s_bars[i], 2, 0);
        lv_obj_set_size(s_bars[i], 4, 2);
        lv_obj_set_pos(s_bars[i], 396 + i * 5, 220);
        lv_obj_add_flag(s_bars[i], LV_OBJ_FLAG_HIDDEN);
    }
    s_text = label(parent, "语音唤醒，或轻触唤醒", C_DIM);
    copy_text(s_caption, "语音唤醒，或轻触唤醒");
    lv_obj_set_pos(s_text, 22, 362);
    lv_obj_set_size(s_text, 436, 48);
    lv_obj_set_style_text_align(s_text, LV_TEXT_ALIGN_CENTER, 0);
    lv_label_set_long_mode(s_text, LV_LABEL_LONG_DOT);
    static const char *const captions[] = {"唤醒", "取消", "清空", "静音"};
    for (int i = 0; i < 4; ++i) {
        lv_obj_t *button = lv_button_create(parent);
        lv_obj_set_pos(button, 18 + i * 114, 420);
        lv_obj_set_size(button, 102, 42);
        lv_obj_set_style_radius(button, 12, 0);
        lv_obj_set_style_bg_color(button, lv_color_hex(i == 0 ? 0x775166 : 0x273443), 0);
        lv_obj_set_style_shadow_width(button, 0, 0);
        lv_obj_t *caption = label(button, captions[i], C_TEXT);
        lv_obj_center(caption);
        if (i == 3) s_mute_label = caption;
        lv_obj_add_event_cb(button, action, LV_EVENT_CLICKED, (void *)(intptr_t)i);
    }
    lv_timer_create(timer_cb, 40, NULL);
    voice_page_tick();
}

void voice_page_tick(void)
{
    if (!s_root) return;
    snapshot_t snapshot;
    portENTER_CRITICAL(&s_lock);
    snapshot = s_pending;
    s_pending.level = 0;
    portEXIT_CRITICAL(&s_lock);
    uint32_t now = lv_tick_get();
    if (snapshot.revision != s_seen) {
        bool phase_changed = s_seen == 0 || s_phase != snapshot.phase;
        s_seen = snapshot.revision;
        s_phase = snapshot.phase;
        s_muted = snapshot.muted;
        set_short_label(s_mute_label, s_muted ? "取消静音" : "静音");
        if (phase_changed) {
            set_short_label(s_status, status_for(s_phase));
            lv_obj_set_style_text_color(s_status, lv_color_hex(s_phase == PH_ERROR ? C_ERROR : C_ACCENT), 0);
        }
        set_caption(snapshot.text[0] ? snapshot.text :
            (s_phase == PH_IDLE ? "语音唤醒，或轻触唤醒" : status_for(s_phase)));
    }
    if (snapshot.wake_serial != s_wake_seen) {
        s_wake_seen = snapshot.wake_serial;
        s_wave_start = now;
        s_sequence_start = now;
        s_waving = true;
    }
    if (s_phase == PH_IDLE || s_phase == PH_ERROR ||
        (s_waving && now - s_wave_start >= avatar_sequences[AVATAR_WAVING].count *
                                             avatar_sequences[AVATAR_WAVING].frame_ms))
        s_waving = false;
    avatar_sequence_id_t sequence = s_waving ? AVATAR_WAVING : sequence_for(s_phase);
    if (sequence != s_sequence) {
        s_sequence = sequence;
        s_sequence_start = s_waving ? s_wave_start : now;
        s_frame = -1;
    }
    const avatar_sequence_t *animation = &avatar_sequences[s_sequence];
    uint32_t step = (now - s_sequence_start) / animation->frame_ms;
    // Failure falls once and holds the final pose until the state changes.
    if (s_sequence == AVATAR_FAILED && step >= animation->count) step = animation->count - 1;
    else step %= animation->count;
    int frame = animation->first + step;
    // Skip invisible-page rendering; timestamps still advance while swiped away.
    if (lv_obj_is_visible(s_root) && frame != s_frame) {
        lv_image_set_src(s_image, &avatar_frames[frame]);
        s_frame = frame;
    }
    s_level = fmaxf(snapshot.level, s_level * 0.65f);
    static const uint8_t shape[WAVE_BARS] = {45, 70, 48, 90, 65, 100, 78, 50, 87, 65, 95, 55};
    for (int i = 0; i < WAVE_BARS; ++i) {
        if (s_phase != PH_SPEAKING && s_phase != PH_LISTENING) {
            lv_obj_add_flag(s_bars[i], LV_OBJ_FLAG_HIDDEN);
        } else {
            lv_obj_remove_flag(s_bars[i], LV_OBJ_FLAG_HIDDEN);
            int height = 2 + (int)(s_level * shape[(i + now / 80) % WAVE_BARS] * 0.24f);
            lv_obj_set_height(s_bars[i], height);
            lv_obj_set_y(s_bars[i], 220 - height / 2);
        }
    }
}
