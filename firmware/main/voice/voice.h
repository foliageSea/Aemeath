#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"
esp_err_t voice_init(void);
// Touch callbacks: bounded nonblocking priority FIFO; full closes the session.
// Cancel/clear/mute fence rounds already received; false means unavailable/full.
bool voice_request_wake(void);
bool voice_request_cancel(void);
bool voice_request_clear(void);
bool voice_request_mute(bool muted);
// Transport-only callbacks. Receive takes ownership of data on all paths.
bool voice_transport_receive(uint32_t epoch, bool binary, uint8_t *data, size_t len);
