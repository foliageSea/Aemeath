// A single /voice owner, IDF6 post-handshake, bounded output and heap ownership
// through HTTPD completion. No codec, JSON parsing or LVGL work on HTTPD.
#include "voice_ws.h"
#include "voice/voice.h"
#include "esp_random.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/idf_additions.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include <stdlib.h>
#include <string.h>
#define RX_MAX 4096
#define TX_MAX 1024
#define SESSION_MAGIC 0x564F4943u
#define TX_DEPTH 48
#define TX_CONTROL_DEPTH 16

typedef struct { uint32_t magic, epoch; int fd; bool owner; } session_t;
typedef struct { uint32_t epoch; int fd; httpd_ws_type_t type; bool close_after; size_t len; uint8_t data[TX_MAX]; } output_t;
static httpd_handle_t s_server;
static QueueHandle_t s_output, s_control;
static TaskHandle_t s_writer;
static portMUX_TYPE s_lock=portMUX_INITIALIZER_UNLOCKED;
static uint32_t s_epoch;
static int s_fd=-1;

bool voice_ws_connected(uint32_t *epoch)
{
    portENTER_CRITICAL(&s_lock); uint32_t e=s_epoch; portEXIT_CRITICAL(&s_lock);
    if (epoch) *epoch=e;
    return e!=0;
}
bool voice_ws_is_client(int fd)
{
    session_t *session=httpd_sess_get_ctx(s_server,fd);
    return session && session->magic==SESSION_MAGIC;
}
void voice_ws_close(uint32_t epoch)
{
    portENTER_CRITICAL(&s_lock);
    int fd=s_epoch==epoch?s_fd:-1;
    // Stop capture immediately. free_ctx is the final ownership cleanup.
    if (fd>=0) { s_epoch=0; s_fd=-1; }
    portEXIT_CRITICAL(&s_lock);
    if (fd>=0) httpd_sess_trigger_close(s_server,fd);
}
static void session_free(void *arg)
{
    session_t *session=arg;
    portENTER_CRITICAL(&s_lock);
    if (session->owner && s_epoch==session->epoch && s_fd==session->fd) { s_epoch=0; s_fd=-1; }
    portEXIT_CRITICAL(&s_lock);
    free(session);
}
static bool enqueue(const output_t *out)
{
    QueueHandle_t queue=out->type==HTTPD_WS_TYPE_BINARY?s_output:s_control;
    return queue && xQueueSend(queue,out,0)==pdTRUE;
}
static bool send_output(uint32_t epoch, bool binary, bool ordered, const void *data, size_t len)
{
    if (!len || len>TX_MAX || !data) return false;
    output_t out={.epoch=epoch,.type=binary?HTTPD_WS_TYPE_BINARY:HTTPD_WS_TYPE_TEXT,.len=len};
    memcpy(out.data,data,len);
    portENTER_CRITICAL(&s_lock);
    out.fd=s_epoch==epoch?s_fd:-1;
    QueueHandle_t queue=(binary || ordered)?s_output:s_control;
    bool sent=out.fd>=0 && queue && xQueueSend(queue,&out,0)==pdTRUE;
    portEXIT_CRITICAL(&s_lock);
    if (!sent && out.fd>=0) voice_ws_close(epoch);
    return sent;
}
bool voice_ws_send(uint32_t epoch, bool binary, const void *data, size_t len)
{
    return send_output(epoch,binary,false,data,len);
}
bool voice_ws_send_ordered(uint32_t epoch, const void *data, size_t len)
{
    return send_output(epoch,false,true,data,len);
}
void voice_ws_discard_audio(uint32_t epoch)
{
    portENTER_CRITICAL(&s_lock);
    // Queue entries contain inline bytes. A reset owns no heap payload and
    // cannot free the single work item already owned by the HTTPD callback.
    // A pending wake is superseded by the local cancellation too.
    if (epoch && s_epoch==epoch && s_output) xQueueReset(s_output);
    portEXIT_CRITICAL(&s_lock);
}
// This function owns out until it has sent (or discarded) it. The queued work
// stores a shallow frame payload; freeing before this callback would be UAF.
static void send_work(void *arg)
{
    output_t *out=arg;
    session_t *session=httpd_sess_get_ctx(s_server,out->fd);
    esp_err_t err=ESP_ERR_INVALID_STATE;
    bool valid=session && session->magic==SESSION_MAGIC && session->epoch==out->epoch;
    if (valid && session->owner) {
        uint32_t epoch; valid=voice_ws_connected(&epoch) && epoch==out->epoch;
    }
    if (valid && httpd_ws_get_fd_info(s_server,out->fd)==HTTPD_WS_CLIENT_WEBSOCKET) {
        httpd_ws_frame_t frame={.final=true,.type=out->type,.payload=out->data,.len=out->len};
        err=httpd_ws_send_frame_async(s_server,out->fd,&frame);
    }
    if (valid && (err!=ESP_OK || out->close_after)) {
        if (session->owner) voice_ws_close(out->epoch);
        else httpd_sess_trigger_close(s_server,out->fd);
    }
    free(out);
    xTaskNotifyGive(s_writer);
}
static bool next_output(output_t *queued)
{
    return xQueueReceive(s_control,queued,0)==pdTRUE || xQueueReceive(s_output,queued,0)==pdTRUE;
}
static void writer(void *arg)
{
    (void)arg;
    output_t queued;
    for (;;) {
        // Only the currently submitted HTTPD work item may precede a control.
        // Never pre-submit the audio FIFO: send_work retains payload ownership.
        if (!next_output(&queued)) {
            vTaskDelay(pdMS_TO_TICKS(5)); continue;
        }
        output_t *out=malloc(sizeof(*out));
        if (!out) { voice_ws_close(queued.epoch); continue; }
        *out=queued;
        if (httpd_queue_work(s_server,send_work,out)!=ESP_OK) { free(out); voice_ws_close(queued.epoch); continue; }
        // At most one work item in flight. On timeout do NOT free its payload
        // or submit more work: the HTTPD callback still owns it.
        while (!ulTaskNotifyTake(pdTRUE,pdMS_TO_TICKS(3000))) voice_ws_close(queued.epoch);
    }
}
static esp_err_t connected(httpd_req_t *req)
{
    session_t *session=calloc(1,sizeof(*session));
    if (!session) return ESP_ERR_NO_MEM;
    session->magic=SESSION_MAGIC; session->fd=httpd_req_to_sockfd(req);
    do { session->epoch=esp_random(); } while (!session->epoch);
    req->sess_ctx=session; req->free_ctx=session_free;
    portENTER_CRITICAL(&s_lock);
    if (!s_epoch) { s_epoch=session->epoch; s_fd=session->fd; session->owner=true; }
    portEXIT_CRITICAL(&s_lock);
    if (!session->owner) {
        static const char busy[]="{\"v\":1,\"type\":\"error\",\"code\":\"busy\",\"message\":\"voice already owned\"}";
        output_t out={.epoch=session->epoch,.fd=session->fd,.type=HTTPD_WS_TYPE_TEXT,.close_after=true,.len=sizeof(busy)-1};
        memcpy(out.data,busy,sizeof(busy)-1);
        if (!enqueue(&out)) return ESP_FAIL;
    }
    return ESP_OK;
}
static esp_err_t receive(httpd_req_t *req)
{
    session_t *session=req->sess_ctx;
    if (!session || !session->owner) return ESP_FAIL;
    httpd_ws_frame_t frame={0};
    esp_err_t err=httpd_ws_recv_frame(req,&frame,0);
    if (err!=ESP_OK) return err;
    if (!frame.final || frame.len>RX_MAX || frame.type==HTTPD_WS_TYPE_CONTINUE) return ESP_FAIL;
    if ((frame.type==HTTPD_WS_TYPE_PING || frame.type==HTTPD_WS_TYPE_PONG || frame.type==HTTPD_WS_TYPE_CLOSE) && frame.len>125) return ESP_FAIL;
    uint8_t *data=malloc(frame.len+1);
    if (!data) return ESP_ERR_NO_MEM;
    frame.payload=data;
    err=httpd_ws_recv_frame(req,&frame,frame.len);
    if (err!=ESP_OK) { free(data); return err; }
    data[frame.len]=0;
    if (frame.type==HTTPD_WS_TYPE_PING || frame.type==HTTPD_WS_TYPE_CLOSE) {
        output_t out={.epoch=session->epoch,.fd=session->fd,.type=frame.type==HTTPD_WS_TYPE_PING?HTTPD_WS_TYPE_PONG:HTTPD_WS_TYPE_CLOSE,.close_after=frame.type==HTTPD_WS_TYPE_CLOSE,.len=frame.len};
        memcpy(out.data,data,frame.len); free(data);
        return enqueue(&out)?ESP_OK:ESP_FAIL;
    }
    if (frame.type==HTTPD_WS_TYPE_PONG) { free(data); return ESP_OK; }
    if (frame.type!=HTTPD_WS_TYPE_TEXT && frame.type!=HTTPD_WS_TYPE_BINARY) { free(data); return ESP_FAIL; }
    return voice_transport_receive(session->epoch,frame.type==HTTPD_WS_TYPE_BINARY,data,frame.len)?ESP_OK:ESP_FAIL;
}
static void delete_output(void)
{
    if (s_output) vQueueDeleteWithCaps(s_output);
    if (s_control) vQueueDeleteWithCaps(s_control);
    s_output=s_control=NULL;
}
esp_err_t voice_ws_register(httpd_handle_t server)
{
    s_server=server;
    // Task-only network buffering must not consume the scarce internal/DMA heap.
    // xQueueCreate() forces ~50 KiB into internal RAM despite available PSRAM.
    s_output=xQueueCreateWithCaps(TX_DEPTH,sizeof(output_t),MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT);
    s_control=xQueueCreateWithCaps(TX_CONTROL_DEPTH,sizeof(output_t),MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT);
    if (!s_output || !s_control) { delete_output(); return ESP_ERR_NO_MEM; }
    if (xTaskCreate(writer,"voice_tx",4096,NULL,4,&s_writer)!=pdPASS) { delete_output(); return ESP_ERR_NO_MEM; }
    const httpd_uri_t uri={.uri="/voice",.method=HTTP_GET,.handler=receive,.is_websocket=true,.handle_ws_control_frames=true,.ws_post_handshake_cb=connected};
    esp_err_t err=httpd_register_uri_handler(server,&uri);
    if (err!=ESP_OK) { vTaskDelete(s_writer); s_writer=NULL; delete_output(); return err; }
    ESP_LOGI("voice_ws","registered /voice; TX %u bytes in PSRAM, free internal %u, PSRAM %u",
        (unsigned)((TX_DEPTH+TX_CONTROL_DEPTH)*sizeof(output_t)),(unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL|MALLOC_CAP_8BIT),
        (unsigned)heap_caps_get_free_size(MALLOC_CAP_SPIRAM));
    return ESP_OK;
}
