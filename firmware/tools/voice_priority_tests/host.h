#pragma once
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
static size_t host_strlcpy(char *dst,const char *src,size_t size) { size_t len=strlen(src); if(size) { size_t n=len<size-1?len:size-1; memcpy(dst,src,n); dst[n]=0; } return len; }
#define strlcpy host_strlcpy
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL -1
#define ESP_ERR_NO_MEM 1
#define ESP_ERR_INVALID_STATE 2
#define ESP_ERR_NOT_FINISHED 3
#define ESP_ERR_TIMEOUT 4
#define ESP_LOGE(...) ((void)0)
#define ESP_LOGI(...) ((void)0)
static const char *esp_err_to_name(int e) { (void)e; return "test"; }
static int64_t esp_timer_get_time(void) { return 1000000; }
typedef int BaseType_t;
typedef int portMUX_TYPE;
typedef void *TaskHandle_t;
#define portMUX_INITIALIZER_UNLOCKED 0
#define portENTER_CRITICAL(x) ((void)(x))
#define portEXIT_CRITICAL(x) ((void)(x))
#define pdTRUE 1
#define pdFALSE 0
#define pdPASS 1
#define pdMS_TO_TICKS(x) (x)
#define portMAX_DELAY UINT32_MAX
#define MALLOC_CAP_SPIRAM 1
#define MALLOC_CAP_8BIT 2
#define MALLOC_CAP_INTERNAL 4
typedef struct { size_t capacity, size, head, count; unsigned char *data; } *QueueHandle_t;
static QueueHandle_t xQueueCreate(size_t capacity, size_t size) {
    QueueHandle_t q=calloc(1,sizeof(*q)); assert(q);
    q->capacity=capacity; q->size=size; q->data=calloc(capacity,size); assert(q->data); return q;
}
static QueueHandle_t xQueueCreateWithCaps(size_t n,size_t size,int caps) { (void)caps; return xQueueCreate(n,size); }
static int xQueueSend(QueueHandle_t q,const void *value,unsigned wait) {
    (void)wait; if (q->count==q->capacity) return pdFALSE;
    memcpy(q->data+((q->head+q->count)%q->capacity)*q->size,value,q->size); q->count++; return pdTRUE;
}
static int xQueuePeek(QueueHandle_t q,void *value,unsigned wait) {
    (void)wait; if (!q->count) return pdFALSE; memcpy(value,q->data+q->head*q->size,q->size); return pdTRUE;
}
static int xQueueReceive(QueueHandle_t q,void *value,unsigned wait) {
    if (!xQueuePeek(q,value,wait)) return pdFALSE;
    q->head=(q->head+1)%q->capacity; q->count--; return pdTRUE;
}
static void xQueueReset(QueueHandle_t q) { q->head=q->count=0; }
static void vQueueDelete(QueueHandle_t q) { free(q->data); free(q); }
#define vQueueDeleteWithCaps vQueueDelete
static int xTaskCreate(void (*f)(void *),const char *name,unsigned size,void *arg,int priority,TaskHandle_t *out) {
    (void)f; (void)name; (void)size; (void)arg; (void)priority; if(out) *out=(void *)1; return pdPASS;
}
static void vTaskDelete(TaskHandle_t task) { (void)task; }
static void vTaskDelay(unsigned ticks) { (void)ticks; }
static void xTaskNotifyGive(TaskHandle_t task) { (void)task; }
static unsigned ulTaskNotifyTake(int clear,unsigned wait) { (void)clear; (void)wait; return 1; }
static size_t heap_caps_get_free_size(int caps) { (void)caps; return 1000000; }
static uint32_t esp_random(void) { return 17; }
// Minimal HTTPD surface; the real transport source and payload lifetime are tested.
typedef void *httpd_handle_t;
typedef enum { HTTPD_WS_TYPE_CONTINUE, HTTPD_WS_TYPE_TEXT, HTTPD_WS_TYPE_BINARY, HTTPD_WS_TYPE_CLOSE=8, HTTPD_WS_TYPE_PING, HTTPD_WS_TYPE_PONG } httpd_ws_type_t;
typedef struct { bool final; httpd_ws_type_t type; uint8_t *payload; size_t len; } httpd_ws_frame_t;
typedef struct { void *sess_ctx; void (*free_ctx)(void *); } httpd_req_t;
typedef struct { const char *uri; int method; esp_err_t (*handler)(httpd_req_t *); bool is_websocket, handle_ws_control_frames; esp_err_t (*ws_post_handshake_cb)(httpd_req_t *); } httpd_uri_t;
#define HTTP_GET 0
#define HTTPD_WS_CLIENT_WEBSOCKET 1
static void *host_session;
static int host_closes, host_sends;
static void *httpd_sess_get_ctx(httpd_handle_t server,int fd) { (void)server; (void)fd; return host_session; }
static int httpd_sess_trigger_close(httpd_handle_t server,int fd) { (void)server; (void)fd; host_closes++; return ESP_OK; }
static int httpd_ws_get_fd_info(httpd_handle_t server,int fd) { (void)server; (void)fd; return HTTPD_WS_CLIENT_WEBSOCKET; }
static int httpd_ws_send_frame_async(httpd_handle_t server,int fd,httpd_ws_frame_t *frame) { (void)server; (void)fd; assert(frame->payload || !frame->len); host_sends++; return ESP_OK; }
static int httpd_queue_work(httpd_handle_t server,void (*work)(void *),void *arg) { (void)server; work(arg); return ESP_OK; }
static int httpd_req_to_sockfd(httpd_req_t *req) { (void)req; return 5; }
static int httpd_ws_recv_frame(httpd_req_t *req,httpd_ws_frame_t *frame,size_t len) { (void)req; (void)frame; (void)len; return ESP_FAIL; }
static int httpd_register_uri_handler(httpd_handle_t server,const httpd_uri_t *uri) { (void)server; (void)uri; return ESP_OK; }
// UI is an observer; no UI work is needed to assert cancellation order.
static void voice_page_update(const char *phase,const char *text,uint32_t turn) { (void)phase; (void)text; (void)turn; }
static void voice_page_level(float level) { (void)level; }
static void voice_page_set_muted(bool muted) { (void)muted; }
static void dashboard_voice_begin(void) {}
static void dashboard_voice_finish(void) {}
