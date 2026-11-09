// Single-page 480x480 dashboard.
#include "dashboard.h"

#include <stdio.h>
#include <string.h>
#include <time.h>

#include "esp_lvgl_port.h"
#include "esp_timer.h"
#include "metrics.h"
#include "net.h"
#include "quota_page.h"
#include "voice_page.h"
#include "display.h"

#define C_BG        0x0B0F14
#define C_CARD      0x151B23
#define C_BORDER    0x222B36
#define C_TRACK     0x30394A  // empty part of progress bars, visible on C_CARD
#define C_TEXT      0xE6EDF3
#define C_DIM       0x7D8590
#define C_CPU       0x4C9AFF
#define C_MEM       0xB37FEB
#define C_GPU       0x3FB950
#define C_UP        0xF0883E
#define C_DOWN      0x58A6FF
#define C_WARN      0xD29922
#define C_HOT       0xF85149

#define HIST_POINTS     60
#define CARD_OPA        LV_OPA_70  // lets the background show through

// tools/make_bg.sh output, embedded via EMBED_FILES
extern const uint8_t bg_480_start[] asm("_binary_bg_480_rgb565_start");
extern const uint8_t bg_480_end[] asm("_binary_bg_480_rgb565_end");
#define AGENT_STALE_US  (3 * 1000 * 1000)

typedef struct {
    lv_obj_t *arc, *value, *caption;
} gauge_t;

static lv_obj_t *s_host, *s_sub, *s_clock, *s_date, *s_pill, *s_pill_lbl;
static gauge_t s_cpu, s_mem, s_gpu;
static lv_obj_t *s_hist, *s_cores;
static lv_chart_series_t *s_ser_cpu, *s_ser_gpu, *s_ser_cores;
static lv_obj_t *s_net_up, *s_net_down, *s_disk_rd, *s_disk_wr, *s_disk_bar, *s_disk_lbl, *s_footer;
static lv_obj_t *s_overlay, *s_ov_title, *s_ov_body, *s_ov_qr;
static uint32_t s_last_seq;
static net_state_t s_last_overlay_state = (net_state_t)-1;
static lv_obj_t *s_tiles, *s_dots[3];
static bool s_voice_active, s_voice_navigation, s_auto_navigation;
static uint32_t s_voice_previous_page, s_voice_finish_deadline;
static bool s_voice_finish_pending;
#define VOICE_IDLE_DWELL_MS 3000

static lv_obj_t *card(lv_obj_t *parent, int x, int y, int w, int h)
{
    lv_obj_t *c = lv_obj_create(parent);
    lv_obj_remove_style_all(c);
    lv_obj_set_pos(c, x, y);
    lv_obj_set_size(c, w, h);
    lv_obj_set_style_bg_color(c, lv_color_hex(C_CARD), 0);
    lv_obj_set_style_bg_opa(c, CARD_OPA, 0);
    lv_obj_set_style_radius(c, 14, 0);
    lv_obj_set_style_border_color(c, lv_color_hex(C_BORDER), 0);
    lv_obj_set_style_border_width(c, 1, 0);
    lv_obj_set_style_pad_all(c, 10, 0);
    lv_obj_remove_flag(c, LV_OBJ_FLAG_SCROLLABLE);
    return c;
}

static lv_obj_t *label(lv_obj_t *parent, const lv_font_t *font, uint32_t color, const char *txt)
{
    lv_obj_t *l = lv_label_create(parent);
    lv_obj_set_style_text_font(l, font, 0);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_label_set_text(l, txt);
    return l;
}

static void gauge_create(gauge_t *g, lv_obj_t *parent, int x, const char *title, uint32_t color)
{
    lv_obj_t *c = card(parent, x, 58, 148, 162);
    lv_obj_t *t = label(c, &lv_font_montserrat_14, C_DIM, title);
    lv_obj_align(t, LV_ALIGN_TOP_LEFT, 0, -2);

    g->arc = lv_arc_create(c);
    lv_obj_set_size(g->arc, 112, 112);
    lv_obj_align(g->arc, LV_ALIGN_CENTER, 0, 2);
    lv_arc_set_rotation(g->arc, 135);
    lv_arc_set_bg_angles(g->arc, 0, 270);
    lv_arc_set_range(g->arc, 0, 100);
    lv_arc_set_value(g->arc, 0);
    lv_obj_remove_style(g->arc, NULL, LV_PART_KNOB);
    lv_obj_remove_flag(g->arc, LV_OBJ_FLAG_CLICKABLE);
    lv_obj_set_style_arc_width(g->arc, 10, LV_PART_MAIN);
    lv_obj_set_style_arc_width(g->arc, 10, LV_PART_INDICATOR);
    lv_obj_set_style_arc_color(g->arc, lv_color_hex(C_BORDER), LV_PART_MAIN);
    lv_obj_set_style_arc_color(g->arc, lv_color_hex(color), LV_PART_INDICATOR);

    g->value = label(c, &lv_font_montserrat_28, C_TEXT, "--");
    lv_obj_align(g->value, LV_ALIGN_CENTER, 0, 0);
    g->caption = label(c, &lv_font_montserrat_14, C_DIM, "");
    lv_obj_align(g->caption, LV_ALIGN_BOTTOM_MID, 0, 4);
}

static void build_header(lv_obj_t *scr)
{
    s_host = label(scr, &lv_font_montserrat_20, C_TEXT, "Aemeath");
    lv_obj_set_pos(s_host, 14, 6);
    lv_label_set_long_mode(s_host, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_width(s_host, 200);
    s_sub = label(scr, &lv_font_montserrat_12, C_DIM, "waiting for agent");
    lv_obj_set_pos(s_sub, 14, 32);
    lv_label_set_long_mode(s_sub, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_width(s_sub, 200);

    s_pill = lv_obj_create(scr);
    lv_obj_remove_style_all(s_pill);
    lv_obj_set_size(s_pill, LV_SIZE_CONTENT, 24);
    lv_obj_set_style_radius(s_pill, 12, 0);
    lv_obj_set_style_bg_opa(s_pill, LV_OPA_COVER, 0);
    lv_obj_set_style_pad_hor(s_pill, 10, 0);
    lv_obj_align(s_pill, LV_ALIGN_TOP_MID, 30, 14);
    s_pill_lbl = label(s_pill, &lv_font_montserrat_12, C_BG, "");
    lv_obj_center(s_pill_lbl);

    s_clock = label(scr, &lv_font_montserrat_28, C_TEXT, "--:--:--");
    lv_obj_align(s_clock, LV_ALIGN_TOP_RIGHT, -14, 2);
    s_date = label(scr, &lv_font_montserrat_12, C_DIM, "");
    lv_obj_align(s_date, LV_ALIGN_TOP_RIGHT, -14, 34);
}

static void build_charts(lv_obj_t *scr)
{
    lv_obj_t *c = card(scr, 8, 228, 300, 124);
    lv_obj_t *t = label(c, &lv_font_montserrat_12, C_DIM, "LOAD 60s");
    lv_obj_align(t, LV_ALIGN_TOP_LEFT, 0, -4);
    lv_obj_t *lc = label(c, &lv_font_montserrat_12, C_CPU, LV_SYMBOL_STOP " CPU");
    lv_obj_align_to(lc, t, LV_ALIGN_OUT_RIGHT_MID, 12, 0);
    lv_obj_t *lg = label(c, &lv_font_montserrat_12, C_GPU, LV_SYMBOL_STOP " GPU");
    lv_obj_align_to(lg, lc, LV_ALIGN_OUT_RIGHT_MID, 10, 0);

    s_hist = lv_chart_create(c);
    lv_obj_set_size(s_hist, 280, 86);
    lv_obj_align(s_hist, LV_ALIGN_BOTTOM_MID, 0, 2);
    lv_chart_set_type(s_hist, LV_CHART_TYPE_LINE);
    lv_chart_set_point_count(s_hist, HIST_POINTS);
    lv_chart_set_axis_range(s_hist, LV_CHART_AXIS_PRIMARY_Y, 0, 100);
    lv_chart_set_div_line_count(s_hist, 3, 0);
    lv_chart_set_update_mode(s_hist, LV_CHART_UPDATE_MODE_SHIFT);
    lv_obj_set_style_bg_opa(s_hist, LV_OPA_TRANSP, 0);
    lv_obj_set_style_border_width(s_hist, 0, 0);
    lv_obj_set_style_pad_all(s_hist, 0, 0);
    lv_obj_set_style_line_color(s_hist, lv_color_hex(C_BORDER), LV_PART_MAIN);
    lv_obj_set_style_line_width(s_hist, 2, LV_PART_ITEMS);
    lv_obj_set_style_size(s_hist, 0, 0, LV_PART_INDICATOR);
    s_ser_cpu = lv_chart_add_series(s_hist, lv_color_hex(C_CPU), LV_CHART_AXIS_PRIMARY_Y);
    s_ser_gpu = lv_chart_add_series(s_hist, lv_color_hex(C_GPU), LV_CHART_AXIS_PRIMARY_Y);
    lv_chart_set_all_values(s_hist, s_ser_cpu, LV_CHART_POINT_NONE);
    lv_chart_set_all_values(s_hist, s_ser_gpu, LV_CHART_POINT_NONE);

    lv_obj_t *k = card(scr, 316, 228, 156, 124);
    lv_obj_t *kt = label(k, &lv_font_montserrat_12, C_DIM, "CORES");
    lv_obj_align(kt, LV_ALIGN_TOP_LEFT, 0, -4);
    s_cores = lv_chart_create(k);
    lv_obj_set_size(s_cores, 136, 86);
    lv_obj_align(s_cores, LV_ALIGN_BOTTOM_MID, 0, 2);
    lv_chart_set_type(s_cores, LV_CHART_TYPE_BAR);
    lv_chart_set_axis_range(s_cores, LV_CHART_AXIS_PRIMARY_Y, 0, 100);
    lv_chart_set_div_line_count(s_cores, 0, 0);
    lv_chart_set_point_count(s_cores, 1);
    lv_obj_set_style_bg_opa(s_cores, LV_OPA_TRANSP, 0);
    lv_obj_set_style_border_width(s_cores, 0, 0);
    lv_obj_set_style_pad_all(s_cores, 0, 0);
    lv_obj_set_style_pad_column(s_cores, 2, 0);
    lv_obj_set_style_radius(s_cores, 2, LV_PART_ITEMS);
    s_ser_cores = lv_chart_add_series(s_cores, lv_color_hex(C_CPU), LV_CHART_AXIS_PRIMARY_Y);
}

static lv_obj_t *io_row(lv_obj_t *parent, int y, const char *icon, uint32_t color)
{
    lv_obj_t *i = label(parent, &lv_font_montserrat_16, color, icon);
    lv_obj_set_pos(i, 0, y);
    lv_obj_t *v = label(parent, &lv_font_montserrat_20, C_TEXT, "--");
    lv_obj_set_pos(v, 22, y - 2);
    return v;
}

static void build_io(lv_obj_t *scr)
{
    lv_obj_t *n = card(scr, 8, 360, 148, 96);
    lv_obj_t *nt = label(n, &lv_font_montserrat_12, C_DIM, "NETWORK");
    lv_obj_set_pos(nt, 0, -4);
    s_net_up = io_row(n, 22, LV_SYMBOL_UP, C_UP);
    s_net_down = io_row(n, 52, LV_SYMBOL_DOWN, C_DOWN);

    lv_obj_t *d = card(scr, 166, 360, 148, 96);
    lv_obj_t *dt = label(d, &lv_font_montserrat_12, C_DIM, "DISK I/O");
    lv_obj_set_pos(dt, 0, -4);
    s_disk_rd = io_row(d, 22, "R", C_DOWN);
    s_disk_wr = io_row(d, 52, "W", C_UP);

    lv_obj_t *u = card(scr, 324, 360, 148, 96);
    lv_obj_t *ut = label(u, &lv_font_montserrat_12, C_DIM, "DISK");
    lv_obj_set_pos(ut, 0, -4);
    s_disk_lbl = label(u, &lv_font_montserrat_20, C_TEXT, "--");
    lv_obj_set_pos(s_disk_lbl, 0, 20);
    s_disk_bar = lv_bar_create(u);
    lv_obj_set_size(s_disk_bar, 128, 10);
    lv_obj_set_pos(s_disk_bar, 0, 58);
    lv_bar_set_range(s_disk_bar, 0, 100);
    lv_obj_set_style_bg_color(s_disk_bar, lv_color_hex(C_TRACK), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(s_disk_bar, LV_OPA_COVER, LV_PART_MAIN);
    lv_obj_set_style_bg_color(s_disk_bar, lv_color_hex(C_MEM), LV_PART_INDICATOR);

    s_footer = label(scr, &lv_font_montserrat_12, C_DIM, "");
    lv_obj_align(s_footer, LV_ALIGN_BOTTOM_MID, 0, -9);
    lv_label_set_long_mode(s_footer, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_width(s_footer, 460);
    lv_obj_set_style_text_align(s_footer, LV_TEXT_ALIGN_CENTER, 0);
}

static void build_overlay(lv_obj_t *scr)
{
    s_overlay = lv_obj_create(scr);
    lv_obj_remove_style_all(s_overlay);
    lv_obj_set_size(s_overlay, LV_PCT(100), LV_PCT(100));
    lv_obj_set_style_bg_color(s_overlay, lv_color_hex(C_BG), 0);
    lv_obj_set_style_bg_opa(s_overlay, LV_OPA_COVER, 0);
    lv_obj_remove_flag(s_overlay, LV_OBJ_FLAG_SCROLLABLE);

    s_ov_title = label(s_overlay, &lv_font_montserrat_28, C_TEXT, "");
    lv_obj_align(s_ov_title, LV_ALIGN_TOP_MID, 0, 28);
    s_ov_qr = lv_qrcode_create(s_overlay);
    lv_qrcode_set_size(s_ov_qr, 200);
    lv_qrcode_set_dark_color(s_ov_qr, lv_color_hex(C_BG));
    lv_qrcode_set_light_color(s_ov_qr, lv_color_white());
    lv_obj_set_style_border_color(s_ov_qr, lv_color_white(), 0);
    lv_obj_set_style_border_width(s_ov_qr, 8, 0);
    lv_obj_align(s_ov_qr, LV_ALIGN_CENTER, 0, -10);
    s_ov_body = label(s_overlay, &lv_font_montserrat_16, C_DIM, "");
    lv_obj_set_style_text_align(s_ov_body, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_align(s_ov_body, LV_ALIGN_BOTTOM_MID, 0, -30);
    lv_obj_add_flag(s_overlay, LV_OBJ_FLAG_HIDDEN);
}

static void fmt_bytes(char *buf, size_t cap, double b)
{
    static const char *u[] = {"B", "K", "M", "G", "T"};
    int i = 0;
    while (b >= 1000 && i < 4) {
        b /= 1024;
        i++;
    }
    snprintf(buf, cap, b < 10 && i ? "%.1f%s" : "%.0f%s", b, u[i]);
}

static void fmt_rate(char *buf, size_t cap, double bps)
{
    if (isnan(bps)) {
        snprintf(buf, cap, "--");
        return;
    }
    static const char *u[] = {"B/s", "KB/s", "MB/s", "GB/s"};
    int i = 0;
    while (bps >= 1000 && i < 3) {
        bps /= 1024;
        i++;
    }
    snprintf(buf, cap, bps < 10 && i ? "%.1f %s" : "%.0f %s", bps, u[i]);
}

static uint32_t load_color(double pct, uint32_t base)
{
    return pct >= 90 ? C_HOT : pct >= 75 ? C_WARN : base;
}

static void gauge_set(gauge_t *g, double pct, uint32_t base, const char *caption)
{
    if (isnan(pct)) {
        lv_arc_set_value(g->arc, 0);
        lv_label_set_text(g->value, "--");
    } else {
        lv_arc_set_value(g->arc, (int)(pct + 0.5));
        lv_label_set_text_fmt(g->value, "%d%%", (int)(pct + 0.5));
        lv_obj_set_style_arc_color(g->arc, lv_color_hex(load_color(pct, base)), LV_PART_INDICATOR);
    }
    lv_label_set_text(g->caption, caption);
}

static void fmt_temp(char *buf, size_t cap, const char *prefix, double t)
{
    if (isnan(t)) {
        snprintf(buf, cap, "%s", prefix);
    } else {
        snprintf(buf, cap, "%s%.0f°C", prefix, t);
    }
}

static void update_metrics(const metrics_t *m)
{
    char a[16], b[16], cap[80];

    lv_label_set_text(s_host, m->host[0] ? m->host : "PC");
    double up = m->uptime;
    if (!isnan(up)) {
        int d = up / 86400, h = ((int)up % 86400) / 3600, mi = ((int)up % 3600) / 60;
        snprintf(cap, sizeof(cap), "%s  up %dd %dh %dm", m->os, d, h, mi);
    } else {
        snprintf(cap, sizeof(cap), "%s", m->os);
    }
    lv_label_set_text(s_sub, cap);

    fmt_temp(cap, sizeof(cap), "", m->cpu_temp);
    if (!isnan(m->cpu_freq) && m->cpu_freq > 0) {
        size_t n = strlen(cap);
        snprintf(cap + n, sizeof(cap) - n, "%s%.1fGHz", n ? "  " : "", m->cpu_freq / 1000.0);
    }
    gauge_set(&s_cpu, m->cpu, C_CPU, cap);

    double mem_pct = (m->mem_total > 0) ? m->mem_used * 100.0 / m->mem_total : NAN;
    if (!isnan(mem_pct)) {
        fmt_bytes(a, sizeof(a), m->mem_used);
        fmt_bytes(b, sizeof(b), m->mem_total);
        snprintf(cap, sizeof(cap), "%s / %s", a, b);
    } else {
        cap[0] = 0;
    }
    gauge_set(&s_mem, mem_pct, C_MEM, cap);

    fmt_temp(cap, sizeof(cap), "", m->gpu_temp);
    if (m->gpu_mem_total > 0) {
        fmt_bytes(a, sizeof(a), m->gpu_mem_used);
        size_t n = strlen(cap);
        snprintf(cap + n, sizeof(cap) - n, "%s%s", n ? "  " : "", a);
    }
    gauge_set(&s_gpu, m->gpu_load, C_GPU, cap);

    lv_chart_set_next_value(s_hist, s_ser_cpu, isnan(m->cpu) ? LV_CHART_POINT_NONE : (int32_t)m->cpu);
    lv_chart_set_next_value(s_hist, s_ser_gpu, isnan(m->gpu_load) ? LV_CHART_POINT_NONE : (int32_t)m->gpu_load);

    int nc = m->ncores > 0 ? m->ncores : 1;
    if (lv_chart_get_point_count(s_cores) != nc) {
        lv_chart_set_point_count(s_cores, nc);
    }
    for (int i = 0; i < nc; i++) {
        lv_chart_set_value_by_id(s_cores, s_ser_cores, i, m->ncores ? (int32_t)m->cores[i] : 0);
    }
    lv_chart_refresh(s_cores);

    fmt_rate(a, sizeof(a), m->net_up);
    lv_label_set_text(s_net_up, a);
    fmt_rate(a, sizeof(a), m->net_down);
    lv_label_set_text(s_net_down, a);
    fmt_rate(a, sizeof(a), m->disk_rd);
    lv_label_set_text(s_disk_rd, a);
    fmt_rate(a, sizeof(a), m->disk_wr);
    lv_label_set_text(s_disk_wr, a);

    if (m->disk_total > 0) {
        fmt_bytes(a, sizeof(a), m->disk_used);
        fmt_bytes(b, sizeof(b), m->disk_total);
        lv_label_set_text_fmt(s_disk_lbl, "%s/%s", a, b);
        int pct = (int)(m->disk_used * 100 / m->disk_total);
        lv_bar_set_value(s_disk_bar, pct, LV_ANIM_ON);
        lv_obj_set_style_bg_color(s_disk_bar, lv_color_hex(load_color(pct, C_MEM)), LV_PART_INDICATOR);
    }

    char foot[200];
    snprintf(foot, sizeof(foot), "%s%s%s%s%s", m->cpu_name, m->gpu_name[0] ? "  |  " : "", m->gpu_name,
             m->ip[0] ? "  |  " : "", m->ip);
    lv_label_set_text(s_footer, foot);
}

static void set_pill(uint32_t color, const char *txt)
{
    lv_obj_set_style_bg_color(s_pill, lv_color_hex(color), 0);
    lv_label_set_text(s_pill_lbl, txt);
}

static void update_overlay(const net_status_t *ns)
{
    if (ns->state == s_last_overlay_state) {
        return;
    }
    s_last_overlay_state = ns->state;
    if (ns->state != NET_PORTAL) {
        lv_obj_add_flag(s_overlay, LV_OBJ_FLAG_HIDDEN);
        return;
    }
    char qr[64];
    snprintf(qr, sizeof(qr), "WIFI:T:nopass;S:%s;;", ns->ap_ssid);
    lv_qrcode_update(s_ov_qr, qr, strlen(qr));
    lv_label_set_text(s_ov_title, LV_SYMBOL_WIFI "  Wi-Fi Setup");
    lv_label_set_text_fmt(s_ov_body,
                          "Scan to join  %s\nthen open  http://" NET_AP_IP "\n%s",
                          ns->ap_ssid,
                          ns->sta_ssid[0] ? "(still retrying saved network)" : "");
    lv_obj_remove_flag(s_overlay, LV_OBJ_FLAG_HIDDEN);
}

static void update_dots(void)
{
    lv_obj_t *active = lv_tileview_get_tile_active(s_tiles);
    int page = active ? lv_obj_get_index(active) : 0;
    for (int i = 0; i < 3; i++) {
        lv_obj_set_style_bg_color(s_dots[i], lv_color_hex(i == page ? C_TEXT : C_BORDER), 0);
    }
}

bool dashboard_show_page(dashboard_page_t page)
{
    if (page < DASHBOARD_PAGE_METRICS || page > DASHBOARD_PAGE_VOICE) {
        return false;
    }
    if (!lvgl_port_lock(0)) {
        return false;
    }
    bool ready = s_tiles != NULL;
    if (ready) {
        // Explicit remote navigation also supersedes automatic restoration.
        if (s_voice_active) s_voice_navigation = true;
        // Use the same tileview as touch gestures; do not rebuild pages.
        lv_tileview_set_tile_by_index(s_tiles, (uint32_t)page, 0, LV_ANIM_OFF);
        update_dots();
    }
    lvgl_port_unlock();
    return ready;
}

static void page_changed(lv_event_t *e)
{
    (void)e;
    if (s_voice_active && !s_auto_navigation) s_voice_navigation = true;
    update_dots();
}

void dashboard_voice_begin(void)
{
    if (!lvgl_port_lock(0)) return;
    s_voice_finish_pending = false;
    if (s_tiles && !s_voice_active) {
        lv_obj_t *active = lv_tileview_get_tile_active(s_tiles);
        s_voice_previous_page = active ? lv_obj_get_index(active) : 0;
        s_voice_active = true;
        s_voice_navigation = false;
        s_auto_navigation = true;
        lv_tileview_set_tile_by_index(s_tiles, DASHBOARD_PAGE_VOICE, 0, LV_ANIM_OFF);
        s_auto_navigation = false;
        update_dots();
        display_voice_begin();
    }
    lvgl_port_unlock();
}

void dashboard_voice_finish(void)
{
    if (!lvgl_port_lock(0)) return;
    if (s_voice_active && !s_voice_finish_pending) {
        s_voice_finish_deadline = lv_tick_get() + VOICE_IDLE_DWELL_MS;
        s_voice_finish_pending = true;
    }
    lvgl_port_unlock();
}

// Runs on the LVGL timer task, with its lock already held. No sleeping or task wait.
static void voice_finish_tick(void)
{
    if (s_voice_finish_pending && (int32_t)(lv_tick_get() - s_voice_finish_deadline) >= 0) {
        s_voice_finish_pending = false;
        // A swipe or explicit page request during the round wins, even if the
        // user navigated back to voice afterwards.
        if (s_tiles && !s_voice_navigation) {
            s_auto_navigation = true;
            lv_tileview_set_tile_by_index(s_tiles, s_voice_previous_page, 0, LV_ANIM_OFF);
            s_auto_navigation = false;
            update_dots();
        }
        s_voice_active = false;
        display_voice_finish();
    }
}

bool dashboard_voice_active(void)
{
    if (!lvgl_port_lock(0)) return false;
    bool active = s_voice_active;
    lvgl_port_unlock();
    return active;
}

static lv_obj_t *page_tile(int col, lv_dir_t dir)
{
    lv_obj_t *t = lv_tileview_add_tile(s_tiles, col, 0, dir);
    lv_obj_remove_flag(t, LV_OBJ_FLAG_SCROLLABLE);
    return t;
}

static void tick_cb(lv_timer_t *t)
{
    (void)t;
    voice_finish_tick();
    update_dots();
    quota_page_tick();

    net_status_t ns;
    net_get_status(&ns);
    update_overlay(&ns);

    static metrics_t m;
    metrics_snapshot(&m);
    bool fresh = m.seq && (esp_timer_get_time() - m.rx_us) < AGENT_STALE_US;
    if (m.seq != s_last_seq) {
        s_last_seq = m.seq;
        update_metrics(&m);
    }

    switch (ns.state) {
    case NET_CONNECTING:
        set_pill(C_WARN, "Wi-Fi connecting");
        break;
    case NET_PORTAL:
        set_pill(C_WARN, "Setup mode");
        break;
    case NET_CONNECTED:
        if (fresh) {
            char buf[48];
            snprintf(buf, sizeof(buf), LV_SYMBOL_WIFI " %d dBm", ns.rssi);
            set_pill(C_GPU, buf);
        } else {
            char buf[48];
            snprintf(buf, sizeof(buf), "No agent  " NET_MDNS_HOST ".local  %s", ns.ip);
            set_pill(m.seq ? C_HOT : C_DIM, buf);
        }
        break;
    }

    time_t now = time(NULL);
    if (m.seq && now > 1600000000) {
        now += m.tz;
        struct tm tm;
        gmtime_r(&now, &tm);
        char buf[24];
        strftime(buf, sizeof(buf), "%H:%M:%S", &tm);
        lv_label_set_text(s_clock, buf);
        strftime(buf, sizeof(buf), "%a %Y-%m-%d", &tm);
        lv_label_set_text(s_date, buf);
    }
}

void dashboard_create(void)
{
    lvgl_port_lock(0);
    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_hex(C_BG), 0);
    lv_obj_remove_flag(scr, LV_OBJ_FLAG_SCROLLABLE);

    static lv_image_dsc_t bg = {
        .header = {
            .magic = LV_IMAGE_HEADER_MAGIC,
            .cf = LV_COLOR_FORMAT_RGB565,
            .w = 480,
            .h = 480,
            .stride = 480 * 2,
        },
    };
    bg.data = bg_480_start;
    bg.data_size = bg_480_end - bg_480_start;
    lv_obj_t *bg_img = lv_image_create(scr);
    lv_image_set_src(bg_img, &bg);
    lv_obj_center(bg_img);

    // Swipe left/right between telemetry, quotas and the voice companion.
    s_tiles = lv_tileview_create(scr);
    lv_obj_set_style_bg_opa(s_tiles, LV_OPA_TRANSP, 0);
    lv_obj_set_scrollbar_mode(s_tiles, LV_SCROLLBAR_MODE_OFF);
    lv_obj_t *pc = page_tile(0, LV_DIR_RIGHT);
    lv_obj_t *quota = page_tile(1, LV_DIR_LEFT | LV_DIR_RIGHT);
    lv_obj_t *voice = page_tile(2, LV_DIR_LEFT);
    lv_obj_add_event_cb(s_tiles, page_changed, LV_EVENT_VALUE_CHANGED, NULL);

    build_header(pc);
    gauge_create(&s_cpu, pc, 8, "CPU", C_CPU);
    gauge_create(&s_mem, pc, 166, "MEMORY", C_MEM);
    gauge_create(&s_gpu, pc, 324, "GPU", C_GPU);
    build_charts(pc);
    build_io(pc);
    quota_page_create(quota);
    voice_page_create(voice);

    for (int i = 0; i < 3; i++) {
        s_dots[i] = lv_obj_create(scr);
        lv_obj_remove_style_all(s_dots[i]);
        lv_obj_set_size(s_dots[i], 6, 6);
        lv_obj_set_style_radius(s_dots[i], 3, 0);
        lv_obj_set_style_bg_opa(s_dots[i], LV_OPA_COVER, 0);
        lv_obj_align(s_dots[i], LV_ALIGN_BOTTOM_MID, (i - 1) * 12, -2);
    }
    build_overlay(scr);
    set_pill(C_DIM, "starting");

    lv_timer_create(tick_cb, 250, NULL);
    lvgl_port_unlock();
}
