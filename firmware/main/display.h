#pragma once

#include "esp_err.h"

// Brings up panel, LVGL port, touch and backlight.
esp_err_t display_start(void);

#include <stdbool.h>

// Screen power + brightness (0..100). Off blanks the panel (DISPOFF) and the backlight.
// Persisted to NVS (debounced) and restored by display_start().
esp_err_t display_set_screen(bool on, int percent);
void display_get_screen(bool *on, int *percent);

// Card background transparency (0..100), persisted with the screen preferences.
bool display_set_card_transparency(int percent);
int display_get_card_transparency(void);

// Temporary voice wake. Never writes NVS; a later display_set_screen() wins.
void display_voice_begin(void);
void display_voice_finish(void);
