// Page 2: subscription quotas from the Magpie gateway.
#include "quota_page.h"

#include <stdio.h>
#include <string.h>
#include <time.h>

#include "esp_timer.h"
#include "nvs.h"
#include "quota.h"

#define C_CARD      0x151B23
#define C_BORDER    0x222B36
#define C_TRACK     0x30394A  // empty part of progress bars, visible on C_CARD
#define C_TEXT      0xE6EDF3
#define C_DIM       0x7D8590
#define C_WARN      0xD29922
#define C_HOT       0xF85149

#define STALE_US    (180LL * 1000 * 1000)
#define MAX_ROWS    (QUOTA_MAX_ACCOUNTS * QUOTA_MAX_WINDOWS)

static const uint32_t s_accents[] = {0x4C9AFF, 0x3FB950, 0xB37FEB, 0xF0883E, 0x58A6FF, 0xDB61A2};

typedef struct {
    lv_obj_t *value, *bar;
    uint32_t accent;
    float used;
    int64_t reset;
    char display[16];
} row_t;

static lv_obj_t *s_usage, *s_updated, *s_list, *s_empty, *s_mode_lbl;
static bool s_show_left;  // false: show used %, true: show remaining %
static row_t s_rows[MAX_ROWS];
static int s_nrows;
static uint32_t s_seq;
static time_t s_last_sec;

static lv_obj_t *label(lv_obj_t *parent, const lv_font_t *font, uint32_t color, const char *txt)
{
    lv_obj_t *l = lv_label_create(parent);
    lv_obj_set_style_text_font(l, font, 0);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_label_set_text(l, txt);
    return l;
}

static lv_obj_t *plain(lv_obj_t *parent)
{
    lv_obj_t *o = lv_obj_create(parent);
    lv_obj_remove_style_all(o);
    lv_obj_remove_flag(o, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_remove_flag(o, LV_OBJ_FLAG_CLICKABLE);
    return o;
}

static void fmt_countdown(char *buf, size_t cap, int64_t reset)
{
    time_t now = time(NULL);
    if (reset <= 0 || now < 1600000000) {
        buf[0] = 0;
        return;
    }
    int64_t d = reset - now;
    if (d <= 0) {
        snprintf(buf, cap, "resetting");
    } else if (d >= 86400) {
        snprintf(buf, cap, "%dd %dh", (int)(d / 86400), (int)(d % 86400 / 3600));
    } else if (d >= 3600) {
        snprintf(buf, cap, "%dh %02dm", (int)(d / 3600), (int)(d % 3600 / 60));
    } else {
        snprintf(buf, cap, "%dm", (int)(d / 60) + 1);
    }
}

static void row_refresh(row_t *r)
{
    char cd[24], buf[64];
    fmt_countdown(cd, sizeof(cd), r->reset);
    char pct[16];
    const char *val = pct;
    if (s_show_left) {
        snprintf(pct, sizeof(pct), "%.0f%% left", 100.0f - r->used);
    } else if (r->display[0]) {
        val = r->display;  // e.g. "3535 / 7000" is a used count
    } else {
        snprintf(pct, sizeof(pct), "%.0f%% used", r->used);
    }
    snprintf(buf, sizeof(buf), cd[0] ? "%s   " LV_SYMBOL_REFRESH " %s" : "%s", val, cd);
    lv_label_set_text(r->value, buf);
}

static uint32_t bar_color(float used, uint32_t accent)
{
    return used >= 90 ? C_HOT : used >= 75 ? C_WARN : accent;
}

// Bar length follows the display mode; colour always warns on high usage.
static void bar_refresh(row_t *r)
{
    float v = s_show_left ? 100.0f - r->used : r->used;
    lv_bar_set_value(r->bar, (int32_t)(v + 0.5f), LV_ANIM_OFF);
    lv_obj_set_style_bg_color(r->bar, lv_color_hex(bar_color(r->used, r->accent)), LV_PART_INDICATOR);
}

static void add_window(lv_obj_t *card, const quota_window_t *w, uint32_t accent)
{
    lv_obj_t *row = plain(card);
    lv_obj_set_size(row, LV_PCT(100), 34);

    lv_obj_t *name = label(row, &lv_font_montserrat_14, C_DIM, w->name);
    lv_obj_align(name, LV_ALIGN_TOP_LEFT, 0, 0);
    lv_label_set_long_mode(name, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_width(name, 220);

    lv_obj_t *bar = lv_bar_create(row);
    lv_obj_set_size(bar, LV_PCT(100), 8);
    lv_obj_align(bar, LV_ALIGN_BOTTOM_MID, 0, 0);
    lv_bar_set_range(bar, 0, 100);
    lv_obj_set_style_bg_color(bar, lv_color_hex(C_TRACK), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(bar, LV_OPA_COVER, LV_PART_MAIN);

    if (s_nrows < MAX_ROWS) {
        row_t *r = &s_rows[s_nrows++];
        r->value = label(row, &lv_font_montserrat_14, C_TEXT, "");
        lv_obj_align(r->value, LV_ALIGN_TOP_RIGHT, 0, 0);
        r->bar = bar;
        r->accent = accent;
        r->used = w->used;
        r->reset = w->reset;
        strlcpy(r->display, w->display, sizeof(r->display));
        row_refresh(r);
        bar_refresh(r);
    }
}

static void add_account(const quota_account_t *a, uint32_t accent)
{
    lv_obj_t *card = lv_obj_create(s_list);
    lv_obj_remove_style_all(card);
    lv_obj_set_size(card, LV_PCT(100), LV_SIZE_CONTENT);
    lv_obj_set_style_bg_color(card, lv_color_hex(C_CARD), 0);
    lv_obj_set_style_bg_opa(card, LV_OPA_COVER, 0);
    lv_obj_set_style_radius(card, 14, 0);
    lv_obj_set_style_border_color(card, lv_color_hex(C_BORDER), 0);
    lv_obj_set_style_border_width(card, 1, 0);
    lv_obj_set_style_border_side(card, LV_BORDER_SIDE_FULL, 0);
    lv_obj_set_style_pad_all(card, 12, 0);
    lv_obj_set_style_pad_row(card, 8, 0);
    lv_obj_set_flex_flow(card, LV_FLEX_FLOW_COLUMN);
    lv_obj_remove_flag(card, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_remove_flag(card, LV_OBJ_FLAG_CLICKABLE);

    lv_obj_t *head = plain(card);
    lv_obj_set_size(head, LV_PCT(100), 24);
    lv_obj_t *dot = plain(head);
    lv_obj_set_size(dot, 10, 10);
    lv_obj_set_style_radius(dot, 5, 0);
    lv_obj_set_style_bg_color(dot, lv_color_hex(accent), 0);
    lv_obj_set_style_bg_opa(dot, LV_OPA_COVER, 0);
    lv_obj_align(dot, LV_ALIGN_LEFT_MID, 0, 0);
    lv_obj_t *name = label(head, &lv_font_montserrat_20, C_TEXT, a->name);
    lv_obj_align(name, LV_ALIGN_LEFT_MID, 18, 0);
    if (a->plan[0]) {
        lv_obj_t *plan = label(head, &lv_font_montserrat_12, C_DIM, a->plan);
        lv_obj_align(plan, LV_ALIGN_RIGHT_MID, 0, 0);
    }
    for (int i = 0; i < a->n; i++) {
        add_window(card, &a->windows[i], accent);
    }
    if (a->n == 0) {
        label(card, &lv_font_montserrat_14, C_DIM, "no windows reported");
    }
}

static void rebuild(const quota_t *q)
{
    lv_obj_clean(s_list);
    s_nrows = 0;
    for (int i = 0; i < q->n; i++) {
        add_account(&q->accounts[i], s_accents[i % (sizeof(s_accents) / sizeof(s_accents[0]))]);
    }
    if (q->has_usage) {
        lv_label_set_text_fmt(s_usage, "Today $%.2f  %lld calls   |   30d $%.0f  %lld calls",
                              q->cost_today, (long long)q->calls_today, q->cost_30d, (long long)q->calls_30d);
    }
    lv_obj_add_flag(s_empty, LV_OBJ_FLAG_HIDDEN);
}

#define NVS_NS      "aemeath_ui"
#define NVS_KEY     "q_left"

static void mode_label_refresh(void)
{
    lv_label_set_text(s_mode_lbl, s_show_left ? "LEFT" : "USED");
}

static void mode_toggle_cb(lv_event_t *e)
{
    s_show_left = !s_show_left;
    mode_label_refresh();
    for (int i = 0; i < s_nrows; i++) {
        row_refresh(&s_rows[i]);
        bar_refresh(&s_rows[i]);
    }
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) == ESP_OK) {
        nvs_set_u8(h, NVS_KEY, s_show_left);
        nvs_commit(h);
        nvs_close(h);
    }
}

static void mode_load(void)
{
    nvs_handle_t h;
    uint8_t v = 0;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) == ESP_OK) {
        nvs_get_u8(h, NVS_KEY, &v);
        nvs_close(h);
    }
    s_show_left = v;
}

void quota_page_create(lv_obj_t *parent)
{
    mode_load();

    lv_obj_t *t = label(parent, &lv_font_montserrat_20, C_TEXT, "AI QUOTA");
    lv_obj_set_pos(t, 14, 6);
    s_usage = label(parent, &lv_font_montserrat_12, C_DIM, "via Magpie gateway");
    lv_obj_set_pos(s_usage, 14, 32);
    s_updated = label(parent, &lv_font_montserrat_12, C_DIM, "");
    lv_obj_align(s_updated, LV_ALIGN_TOP_RIGHT, -14, 34);

    // Tap to switch between used and remaining.
    lv_obj_t *mode = lv_button_create(parent);
    lv_obj_remove_style_all(mode);
    lv_obj_set_size(mode, 76, 26);
    lv_obj_set_style_radius(mode, 13, 0);
    lv_obj_set_style_bg_color(mode, lv_color_hex(C_BORDER), 0);
    lv_obj_set_style_bg_color(mode, lv_color_hex(C_DIM), LV_STATE_PRESSED);
    lv_obj_set_style_bg_opa(mode, LV_OPA_COVER, 0);
    lv_obj_set_ext_click_area(mode, 10);
    lv_obj_align(mode, LV_ALIGN_TOP_RIGHT, -12, 6);
    lv_obj_add_event_cb(mode, mode_toggle_cb, LV_EVENT_CLICKED, NULL);
    s_mode_lbl = label(mode, &lv_font_montserrat_14, C_TEXT, "");
    lv_obj_center(s_mode_lbl);
    mode_label_refresh();

    s_list = lv_obj_create(parent);
    lv_obj_remove_style_all(s_list);
    lv_obj_set_pos(s_list, 8, 54);
    lv_obj_set_size(s_list, 464, 480 - 54 - 12);
    lv_obj_set_flex_flow(s_list, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(s_list, 8, 0);
    lv_obj_set_style_pad_bottom(s_list, 8, 0);
    lv_obj_set_scroll_dir(s_list, LV_DIR_VER);
    lv_obj_set_scrollbar_mode(s_list, LV_SCROLLBAR_MODE_ACTIVE);

    s_empty = label(parent, &lv_font_montserrat_16, C_DIM, "Waiting for quota data from the agent\n(Magpie gateway on the PC)");
    lv_obj_set_style_text_align(s_empty, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_center(s_empty);
}

void quota_page_tick(void)
{
    static quota_t q;
    quota_snapshot(&q);
    if (q.seq != s_seq) {
        s_seq = q.seq;
        rebuild(&q);
    }

    time_t now = time(NULL);
    if (now == s_last_sec) {
        return;
    }
    s_last_sec = now;
    for (int i = 0; i < s_nrows; i++) {
        row_refresh(&s_rows[i]);
    }
    if (q.seq) {
        int64_t age = esp_timer_get_time() - q.rx_us;
        if (age > STALE_US) {
            lv_obj_set_style_text_color(s_updated, lv_color_hex(C_HOT), 0);
            lv_label_set_text_fmt(s_updated, "stale %lldm", (long long)(age / 60000000));
        } else {
            lv_obj_set_style_text_color(s_updated, lv_color_hex(C_DIM), 0);
            lv_label_set_text_fmt(s_updated, "updated %llds ago", (long long)(age / 1000000));
        }
    }
}
