#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_http_server.h"
esp_err_t voice_ws_register(httpd_handle_t server);
bool voice_ws_connected(uint32_t *epoch);
// Bounded control FIFO precedes queued audio; one HTTPD send may be in flight.
// Copies the payload. Either FIFO overflowing closes the matching epoch.
bool voice_ws_send(uint32_t epoch, bool binary, const void *data, size_t len);
// Wake must follow previously captured mic frames instead of enabling capture
// before those frames arrive. Uses the same bounded FIFO as binary audio.
bool voice_ws_send_ordered(uint32_t epoch, const void *data, size_t len);
// Local cancellation drops queued mic and superseded wake; in-flight work owns itself.
void voice_ws_discard_audio(uint32_t epoch);
void voice_ws_close(uint32_t epoch);
bool voice_ws_is_client(int fd);
