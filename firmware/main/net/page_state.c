// Page observations are independent of commands: never navigate from this module.
#include <stdio.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "net.h"
#include "page_state.h"
#include "voice_ws.h"

static portMUX_TYPE s_page_lock = portMUX_INITIALIZER_UNLOCKED;
static int s_page = -1; // unknown until LVGL publishes an actual active tile
static bool s_work_pending;
static TaskHandle_t s_page_task;
static httpd_handle_t s_page_server;

void server_publish_page(int page)
{
    if (page < -1 || page > 2) return;
    portENTER_CRITICAL(&s_page_lock);
    bool changed = s_page != page;
    s_page = page;
    TaskHandle_t task = s_page_task;
    portEXIT_CRITICAL(&s_page_lock);
    // Task notifications do not wait. In particular, never queue HTTPD work while
    // LVGL is locked: HTTPD may itself be waiting for LVGL to apply a command.
    if (changed && task) xTaskNotifyGive(task);
}

void server_broadcast_page(void)
{
    portENTER_CRITICAL(&s_page_lock);
    TaskHandle_t task = s_page_task;
    portEXIT_CRITICAL(&s_page_lock);
    if (task) xTaskNotifyGive(task);
}

// HTTPD task: copy only the snapshot; never read LVGL or acquire its lock.
static void page_state_send(void *arg)
{
    (void)arg;
    portENTER_CRITICAL(&s_page_lock);
    int page = s_page;
    s_work_pending = false;
    portEXIT_CRITICAL(&s_page_lock);
    if (page < 0 || page > 2) return;
    static const char *names[] = {"metrics", "quota", "voice"};
    char msg[64];
    int len = snprintf(msg, sizeof(msg), "{\"v\":1,\"type\":\"page_state\",\"page\":\"%s\"}", names[page]);
    httpd_ws_frame_t frame = {.type = HTTPD_WS_TYPE_TEXT, .payload = (uint8_t *)msg, .len = len, .final = true};
    int fds[CONFIG_LWIP_MAX_SOCKETS];
    size_t n = sizeof(fds) / sizeof(fds[0]);
    if (httpd_get_client_list(s_page_server, &n, fds) != ESP_OK) return;
    for (size_t i = 0; i < n; ++i) {
        if (!voice_ws_is_client(fds[i]) && httpd_ws_get_fd_info(s_page_server, fds[i]) == HTTPD_WS_CLIENT_WEBSOCKET) {
            httpd_ws_send_frame_async(s_page_server, fds[i], &frame);
        }
    }
}

static void page_state_queue(void)
{
    portENTER_CRITICAL(&s_page_lock);
    bool queue = s_page >= 0 && !s_work_pending;
    if (queue) s_work_pending = true;
    portEXIT_CRITICAL(&s_page_lock);
    if (queue && httpd_queue_work(s_page_server, page_state_send, NULL) != ESP_OK) {
        portENTER_CRITICAL(&s_page_lock);
        s_work_pending = false;
        portEXIT_CRITICAL(&s_page_lock);
        // Retry from this dedicated task only, never from LVGL/HTTPD/timer tasks.
        vTaskDelay(pdMS_TO_TICKS(100));
        server_broadcast_page();
    }
}

static void page_state_task(void *arg)
{
    (void)arg;
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        // At most one queued callback; it reads the newest snapshot when run.
        page_state_queue();
    }
}

void server_page_state_start(httpd_handle_t server)
{
    s_page_server = server;
    TaskHandle_t task;
    // Required control-plane state is tiny; fail explicitly rather than silently
    // running a firmware that cannot synchronize the displayed page.
    BaseType_t created = xTaskCreate(page_state_task, "page_state", 3072, NULL, 3, &task);
    ESP_ERROR_CHECK(created == pdPASS ? ESP_OK : ESP_ERR_NO_MEM);
    portENTER_CRITICAL(&s_page_lock);
    s_page_task = task;
    portEXIT_CRITICAL(&s_page_lock);
    server_broadcast_page();
}
