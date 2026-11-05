#pragma once

#include "lvgl.h"

// Builds the Magpie quota page into a tileview tile.
void quota_page_create(lv_obj_t *parent);
// Call from the UI timer (LVGL lock held).
void quota_page_tick(void);
