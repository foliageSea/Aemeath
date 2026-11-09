#pragma once
#include <stdbool.h>
static inline bool lvgl_port_lock(unsigned timeout) {(void)timeout;return true;}
static inline void lvgl_port_unlock(void) {}
