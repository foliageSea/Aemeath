file(GLOB_RECURSE SRCS "*.c")
idf_component_register(SRCS ${SRCS}
                       INCLUDE_DIRS "." "net" "ui"
                       REQUIRES esp_driver_gpio esp_driver_i2c esp_driver_ledc esp_lcd esp_wifi esp_netif
                                esp_event nvs_flash esp_http_server lwip esp_timer)
