#pragma once
#include <stdint.h>
#include "lvgl.h"
static inline int64_t esp_timer_get_time(void) {return (int64_t)lv_tick_get()*1000;}
