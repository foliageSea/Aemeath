#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_http_server.h"
esp_err_t voice_ws_register(httpd_handle_t server);
bool voice_ws_connected(uint32_t *epoch);
bool voice_ws_send(uint32_t epoch, bool binary, const void *data, size_t len);
void voice_ws_close(uint32_t epoch);
bool voice_ws_is_client(int fd);
