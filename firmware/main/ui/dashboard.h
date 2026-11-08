#pragma once

#include <stdbool.h>

typedef enum {
    DASHBOARD_PAGE_METRICS = 0,
    DASHBOARD_PAGE_QUOTA = 1,
} dashboard_page_t;

// Builds the dashboard and starts its refresh timer.
void dashboard_create(void);

// Switches existing tiles under the LVGL lock; safe to call from the network task.
// Returns false for an invalid page or if the dashboard is not ready.
bool dashboard_show_page(dashboard_page_t page);
