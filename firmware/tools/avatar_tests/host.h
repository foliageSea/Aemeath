#pragma once
#include <stdbool.h>
#include "lvgl.h"

extern bool host_accept_commands, host_last_muted;
extern unsigned host_wakes, host_cancels, host_clears, host_mutes;
extern unsigned host_screen_begins, host_screen_finishes;
void host_advance(uint32_t ms);
void host_snapshot(const char *directory, const char *name, lv_obj_t *root);
void voice_checks(const char *directory);
void dashboard_checks(void);
