#pragma once

#include "lvgl.h"

// Builds the Magpie quota page into a tileview tile.
void quota_page_create(lv_obj_t *parent, lv_style_t *card_background);
// Call from the UI timer (LVGL lock held).
void quota_page_tick(void);
