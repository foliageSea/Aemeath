#pragma once

#include <stdbool.h>

typedef enum {
    DASHBOARD_PAGE_METRICS = 0,
    DASHBOARD_PAGE_QUOTA = 1,
    DASHBOARD_PAGE_VOICE = 2,
} dashboard_page_t;

// Builds the dashboard and starts its refresh timer.
void dashboard_create(void);

// Switches existing tiles under the LVGL lock; safe to call from the network task.
// Returns false for an invalid page or if the dashboard is not ready.
bool dashboard_show_page(dashboard_page_t page);

// Thread safe. One automatic entry per round; explicit navigation takes priority.
void dashboard_voice_begin(void);
// Schedule a 3s idle dwell before restoring page/screen. Repeated finish is idempotent;
// begin cancels a pending finish. Manual navigation during the round/dwell wins.
void dashboard_voice_finish(void);
bool dashboard_voice_active(void);
