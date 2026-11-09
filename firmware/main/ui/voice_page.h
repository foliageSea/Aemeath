#pragma once
#include <stdbool.h>
#include <stdint.h>
#include "lvgl.h"

// create/tick run on the LVGL task or under its lock. The page owns a 40ms timer.
void voice_page_create(lv_obj_t *parent);
// Thread-safe bounded snapshots; these functions never call LVGL or block.
// NULL text preserves the last caption; an empty string clears it.
void voice_page_update(const char *state, const char *text, uint32_t turn);
// Normalized microphone energy while listening or rendered-audio energy while speaking.
// Range 0..1; NaN/negative becomes silence. Peaks decay on the UI timer.
void voice_page_level(float level);
// Transport snapshot, including reset to false on a new connection. No LVGL calls.
void voice_page_set_muted(bool muted);
void voice_page_tick(void);
