#pragma once

#include "esp_err.h"

// Brings up panel, LVGL port, touch and backlight.
esp_err_t display_start(void);

#include <stdbool.h>

// Screen power + brightness (0..100). Off blanks the panel (DISPOFF) and the backlight.
// Persisted to NVS (debounced) and restored by display_start().
esp_err_t display_set_screen(bool on, int percent);
void display_get_screen(bool *on, int *percent);
