#include "board.h"
#include "dashboard.h"
#include "display.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "metrics.h"
#include "net.h"
#include "nvs_flash.h"
#include "quota.h"
#include "voice/voice.h"

static const char *TAG = "main";

#define FORGET_HOLD_MS 3000

static void nvs_init(void)
{
    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        err = nvs_flash_init();
    }
    ESP_ERROR_CHECK(err);
}

void app_main(void)
{
    nvs_init();
    metrics_init();
    quota_init();
    ESP_ERROR_CHECK(board_init());
    ESP_ERROR_CHECK(display_start());
    dashboard_create();
    ESP_ERROR_CHECK(voice_init());
    net_start();

    // Holding BOOT for 3 s forgets Wi-Fi and reboots into the setup portal.
    int held_ms = 0;
    for (;;) {
        vTaskDelay(pdMS_TO_TICKS(100));
        held_ms = board_boot_pressed() ? held_ms + 100 : 0;
        if (held_ms == FORGET_HOLD_MS) {
            ESP_LOGW(TAG, "BOOT held, forgetting Wi-Fi");
            net_forget_credentials();
        }
    }
}
