#pragma once
#include "esp_http_server.h"
// Optional routes. Status remains available even if worker allocation fails.
esp_err_t background_http_register(httpd_handle_t server);
