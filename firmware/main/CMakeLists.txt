file(GLOB_RECURSE SRCS "*.c")
list(FILTER SRCS EXCLUDE REGEX "/tests/")
idf_component_register(SRCS ${SRCS}
                       INCLUDE_DIRS "." "net" "ui" "audio" "voice"
                       EMBED_FILES "assets/bg_480.rgb565" "assets/avatar_frames.bin"
                       REQUIRES esp_driver_gpio esp_driver_i2c esp_driver_i2s esp_driver_ledc esp_lcd esp_wifi esp_netif
                                esp_event nvs_flash esp_http_server lwip esp_timer)
# lv_font_conv supports both flat component and nested LVGL include layouts.
target_compile_definitions(${COMPONENT_LIB} PRIVATE LV_LVGL_H_INCLUDE_SIMPLE)
