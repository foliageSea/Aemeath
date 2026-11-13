#pragma once

#include <stdbool.h>
#include <stdint.h>

typedef enum {
    DASHBOARD_PAGE_METRICS = 0,
    DASHBOARD_PAGE_QUOTA = 1,
    DASHBOARD_PAGE_VOICE = 2,
} dashboard_page_t;

// Builds the dashboard and starts its refresh timer.
void dashboard_create(void);

// Complete immutable 480x480 RGB565_LE pixels, or NULL for the plain default.
// Never takes ownership. On success the previous pixels may be freed by caller.
// Uses the same lock as the port timer/refresh; old source caches are retired
// only after all software draw workers from the previous refresh have completed.
bool dashboard_set_background(const uint8_t *pixels, uint32_t timeout_ms);

// Switches existing tiles under the LVGL lock; safe to call from the network task.
// Returns false for an invalid page or if the dashboard is not ready.
bool dashboard_show_page(dashboard_page_t page);

// Thread safe. One automatic entry per round; explicit navigation takes priority.
void dashboard_voice_begin(void);
// Schedule a 3s idle dwell before restoring page/screen. Repeated finish is idempotent;
// begin cancels a pending finish. Manual navigation during the round/dwell wins.
void dashboard_voice_finish(void);
bool dashboard_voice_active(void);
