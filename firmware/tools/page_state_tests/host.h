#pragma once
#include <assert.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <pthread.h>
#include <string.h>
typedef pthread_mutex_t portMUX_TYPE;
#define portMUX_INITIALIZER_UNLOCKED PTHREAD_MUTEX_INITIALIZER
#define portENTER_CRITICAL(lock) assert(pthread_mutex_lock(lock) == 0)
#define portEXIT_CRITICAL(lock) assert(pthread_mutex_unlock(lock) == 0)
typedef void *TaskHandle_t;
typedef int BaseType_t;
typedef void *httpd_handle_t;
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_ERR_NO_MEM 1
#define ESP_ERROR_CHECK(result) assert((result) == ESP_OK)
#define pdPASS 1
#define pdTRUE 1
#define portMAX_DELAY UINT32_MAX
#define pdMS_TO_TICKS(ms) (ms)
#define CONFIG_LWIP_MAX_SOCKETS 8
#define HTTPD_WS_TYPE_TEXT 1
#define HTTPD_WS_CLIENT_WEBSOCKET 2
typedef struct { int type; uint8_t *payload; size_t len; bool final; } httpd_ws_frame_t;
static unsigned notifications, queued, sends, retries;
static bool lvgl_locked, queue_fail;
static void (*pending)(void *);
static void *pending_arg;
static char last_message[64];
static int xTaskCreate(void (*fn)(void *), const char *name, unsigned stack, void *arg, int priority, TaskHandle_t *task)
{ (void)fn; (void)name; (void)stack; (void)arg; (void)priority; *task = (void *)1; return pdPASS; }
static void xTaskNotifyGive(TaskHandle_t task) { assert(task); ++notifications; }
static unsigned ulTaskNotifyTake(int clear, unsigned wait) { (void)clear; (void)wait; return 1; }
static void vTaskDelay(unsigned ticks) { assert(ticks == 100); ++retries; }
static int httpd_queue_work(httpd_handle_t server, void (*fn)(void *), void *arg)
{
    assert(server && !lvgl_locked); // A blocking queue is forbidden on publication paths.
    ++queued;
    if (queue_fail) return 1;
    assert(!pending); pending = fn; pending_arg = arg; return ESP_OK;
}
static int httpd_get_client_list(httpd_handle_t server, size_t *n, int *fds)
{ assert(server && *n >= 4); fds[0] = 10; fds[1] = 11; fds[2] = 20; fds[3] = 30; *n = 4; return ESP_OK; }
static int httpd_ws_get_fd_info(httpd_handle_t server, int fd)
{ assert(server); return fd == 30 ? 0 : HTTPD_WS_CLIENT_WEBSOCKET; }
static int httpd_ws_send_frame_async(httpd_handle_t server, int fd, httpd_ws_frame_t *frame)
{
    assert(server && (fd == 10 || fd == 11));
    assert(frame->type == HTTPD_WS_TYPE_TEXT && frame->final && frame->len < sizeof(last_message));
    memcpy(last_message, frame->payload, frame->len); last_message[frame->len] = 0;
    ++sends; return ESP_OK;
}
