#pragma once
#include "esp_http_server.h"

// Start once after HTTPD starts; no LVGL access from either function.
void server_page_state_start(httpd_handle_t server);
// Force the latest known snapshot to all /ws clients (including a new handshake).
void server_broadcast_page(void);
