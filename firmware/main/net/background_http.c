// Only small status/preflight responses run on the HTTPD task. One bounded
// async worker owns the whole body/codec/flash/image-send lifecycle.
#include "background_http.h"
#include "background.h"
#include "background_format.h"
#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/select.h>
#include <sys/socket.h>
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"

#define REQUEST_US (30LL * 1000000)
static const char *TAG = "background_http";
typedef enum { JOB_IMAGE, JOB_PUT, JOB_DELETE } job_kind_t;
typedef struct {
    httpd_req_t *req;
    job_kind_t kind;
    int64_t deadline;
    uint64_t revision;
    uint8_t sha[32];
} job_t;
typedef struct { int64_t deadline; } transport_t;
static QueueHandle_t s_queue;
static bool s_busy;
static portMUX_TYPE s_admission_lock = portMUX_INITIALIZER_UNLOCKED;

static bool claim(void)
{
    portENTER_CRITICAL(&s_admission_lock);
    bool acquired = !s_busy;
    if (acquired) s_busy = true;
    portEXIT_CRITICAL(&s_admission_lock);
    return acquired;
}
static void release(void)
{
    portENTER_CRITICAL(&s_admission_lock);
    s_busy = false;
    portEXIT_CRITICAL(&s_admission_lock);
}
static esp_err_t failure(httpd_req_t *req, const char *status, const char *error)
{
    char body[192];
    snprintf(body, sizeof(body), "{\"error\":\"%s\"}", error); // only fixed ASCII errors
    httpd_resp_set_status(req, status);
    httpd_resp_set_type(req, "application/json");
    httpd_resp_set_hdr(req, "Cache-Control", "no-store");
    httpd_resp_set_hdr(req, "Connection", "close");
    return httpd_resp_sendstr(req, body);
}
static esp_err_t reject(httpd_req_t *req, const char *status, const char *error)
{
    failure(req, status, error);
    // A rejected PUT may have an unread body. Returning failure closes it rather
    // than blocking HTTPD while it tries to drain attacker-controlled content.
    return ESP_FAIL;
}
static esp_err_t state_response(httpd_req_t *req)
{
    background_status_t s;
    background_snapshot(&s);
    char etag[32], body[512], error[112] = "";
    snprintf(etag, sizeof(etag), "\"%" PRIu64 "\"", s.revision);
    if (s.error[0]) snprintf(error, sizeof(error), ",\"error\":\"%s\"", s.error);
    int len = snprintf(body, sizeof(body),
        "{\"v\":1,\"supported\":%s,\"present\":%s,\"displayed\":%s,\"revision\":%" PRIu64
        ",\"sha256\":\"%s\",\"width\":%" PRIu32 ",\"height\":%" PRIu32 ",\"bytes\":%" PRIu32
        ",\"maxBytes\":196608,\"scope\":\"shared\"%s}",
        s.supported ? "true" : "false", s.present ? "true" : "false", s.displayed ? "true" : "false",
        s.revision, s.sha256, s.width, s.height, s.bytes, error);
    if (len < 0 || (size_t)len >= sizeof(body)) return failure(req, "503 Service Unavailable", "status encoding failed");
    httpd_resp_set_type(req, "application/json");
    httpd_resp_set_hdr(req, "ETag", etag);
    httpd_resp_set_hdr(req, "Cache-Control", "no-store");
    return httpd_resp_send(req, body, len);
}
static esp_err_t result_response(httpd_req_t *req, background_result_t result)
{
    switch (result) {
    case BG_OK: return state_response(req);
    case BG_CONFLICT: return failure(req, "412 Precondition Failed", "background revision changed");
    case BG_INVALID: return failure(req, "422 Unprocessable Content", "invalid or unsupported JPEG/digest");
    case BG_NOT_FOUND: return failure(req, "404 Not Found", "no background image");
    case BG_TIMEOUT: return failure(req, "408 Request Timeout", "background request timed out");
    case BG_UNSUPPORTED: return failure(req, "503 Service Unavailable", "upgrade firmware partition table");
    case BG_NO_MEMORY: return failure(req, "503 Service Unavailable", "insufficient background memory");
    default: return failure(req, "503 Service Unavailable", "background IO/display unavailable; query status");
    }
}

// Per-session immutable deadline. MSG_DONTWAIT + select enforces one absolute
// deadline even across partial IO, header sends and a peer trickling forever.
// Transport context belongs to HTTPD until socket close, not to a worker stack.
static int deadline_io(httpd_handle_t hd, int fd, void *buf, size_t len, int flags, bool writing)
{
    const transport_t *ctx = httpd_sess_get_transport_ctx(hd, fd);
    if (!ctx) return HTTPD_SOCK_ERR_FAIL;
    for (;;) {
        int64_t left = ctx->deadline - esp_timer_get_time();
        if (left <= 0) return HTTPD_SOCK_ERR_TIMEOUT;
        struct timeval timeout = {.tv_sec = left / 1000000, .tv_usec = left % 1000000};
        fd_set set;
        FD_ZERO(&set); FD_SET(fd, &set);
        int ready = select(fd + 1, writing ? NULL : &set, writing ? &set : NULL, NULL, &timeout);
        if (ready == 0) return HTTPD_SOCK_ERR_TIMEOUT;
        if (ready < 0) { if (errno == EINTR) continue; return HTTPD_SOCK_ERR_FAIL; }
        int n = writing ? send(fd, buf, len, flags | MSG_DONTWAIT) : recv(fd, buf, len, flags | MSG_DONTWAIT);
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR)) continue;
        return n < 0 || (writing && !n) ? HTTPD_SOCK_ERR_FAIL : n;
    }
}
static int deadline_send(httpd_handle_t hd, int fd, const char *buf, size_t len, int flags)
{
    return deadline_io(hd, fd, (void *)buf, len, flags, true);
}
static int deadline_recv(httpd_handle_t hd, int fd, char *buf, size_t len, int flags)
{
    return deadline_io(hd, fd, buf, len, flags, false);
}
static background_result_t receive_put(const job_t *job)
{
    httpd_req_t *req = job->req;
    size_t len = req->content_len;
    uint8_t *data = heap_caps_malloc(len, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (!data) return BG_NO_MEMORY;
    background_result_t result = BG_OK;
    size_t received = 0;
    while (received < len) {
        if (esp_timer_get_time() >= job->deadline) { result = BG_TIMEOUT; break; }
        size_t n = len - received;
        if (n > BACKGROUND_SECTOR_BYTES) n = BACKGROUND_SECTOR_BYTES;
        int actual = httpd_req_recv(req, (char *)data + received, n);
        if (actual == HTTPD_SOCK_ERR_TIMEOUT) { result = BG_TIMEOUT; break; }
        if (actual <= 0 || (size_t)actual > n) { result = BG_INVALID; break; }
        received += (size_t)actual;
    }
    if (result == BG_OK) result = background_put(job->revision, data, len, job->sha);
    free(data);
    return result;
}
static esp_err_t send_image(const job_t *job)
{
    uint8_t *data;
    size_t len;
    background_result_t result = background_read_image(&data, &len);
    if (result != BG_OK) return result_response(job->req, result);
    background_status_t s;
    background_snapshot(&s);
    char etag[32];
    snprintf(etag, sizeof(etag), "\"%" PRIu64 "\"", s.revision);
    httpd_resp_set_type(job->req, "image/jpeg");
    httpd_resp_set_hdr(job->req, "ETag", etag);
    httpd_resp_set_hdr(job->req, "Cache-Control", "no-store");
    esp_err_t err = ESP_OK;
    for (size_t pos = 0; pos < len && err == ESP_OK;) {
        if (esp_timer_get_time() >= job->deadline) { err = ESP_ERR_TIMEOUT; break; }
        size_t n = len - pos < BACKGROUND_SECTOR_BYTES ? len - pos : BACKGROUND_SECTOR_BYTES;
        err = httpd_resp_send_chunk(job->req, (const char *)data + pos, n);
        pos += n;
    }
    if (err == ESP_OK) err = httpd_resp_send_chunk(job->req, NULL, 0);
    free(data);
    return err;
}
static void process_job(const job_t *job)
{
    httpd_req_t *req = job->req;
    int fd = httpd_req_to_sockfd(req);
    transport_t *transport = malloc(sizeof(*transport));
    httpd_resp_set_hdr(req, "Connection", "close");
    if (!transport || httpd_sess_get_transport_ctx(req->handle, fd)) {
        free(transport);
        failure(req, "503 Service Unavailable", "background transport unavailable");
    } else {
        transport->deadline = job->deadline;
        httpd_sess_set_transport_ctx(req->handle, fd, transport, free);
        esp_err_t io = httpd_sess_set_send_override(req->handle, fd, deadline_send);
        if (io == ESP_OK) io = httpd_sess_set_recv_override(req->handle, fd, deadline_recv);
        if (io != ESP_OK) failure(req, "503 Service Unavailable", "background transport setup failed");
        else {
            background_set_deadline(job->deadline);
            if (job->kind == JOB_IMAGE) send_image(job);
            else if (job->kind == JOB_PUT) result_response(req, receive_put(job));
            else result_response(req, background_delete(job->revision));
            background_set_deadline(0);
        }
    }
    // Always close: no unread/trailing PUT body may become another HTTP request.
    // Shutdown before complete prevents fd reuse while worker still owns it.
    shutdown(fd, SHUT_RDWR);
    esp_err_t completed = httpd_req_async_handler_complete(req);
    if (completed != ESP_OK) ESP_LOGE(TAG, "async completion failed: %s", esp_err_to_name(completed));
}
static void worker(void *unused)
{
    (void)unused;
    job_t job;
    for (;;) if (xQueueReceive(s_queue, &job, portMAX_DELAY) == pdTRUE) { process_job(&job); release(); }
}
static esp_err_t status_get(httpd_req_t *req)
{
    if (req->content_len) return reject(req, "422 Unprocessable Content", "GET must not contain a body");
    return state_response(req);
}
static esp_err_t submit(httpd_req_t *req, job_kind_t kind)
{
    background_status_t status;
    background_snapshot(&status);
    if (!status.supported) return reject(req, "503 Service Unavailable", "upgrade firmware partition table");
    if (!s_queue) return reject(req, "503 Service Unavailable", "background worker unavailable");
    job_t job = {.kind = kind, .deadline = esp_timer_get_time() + REQUEST_US};
    if (kind == JOB_IMAGE && httpd_req_get_hdr_value_len(req, "If-Match")) {
        char etag[32];
        uint64_t expected;
        if (httpd_req_get_hdr_value_str(req, "If-Match", etag, sizeof(etag)) != ESP_OK || !background_parse_etag(etag, &expected) || expected != status.revision)
            return reject(req, "412 Precondition Failed", "background revision changed");
    }
    if (kind != JOB_IMAGE) {
        char text[80];
        if (httpd_req_get_hdr_value_str(req, "If-Match", text, sizeof(text)) != ESP_OK || !background_parse_etag(text, &job.revision) || job.revision != status.revision)
            return reject(req, "412 Precondition Failed", "valid current If-Match required");
        if (kind == JOB_PUT) {
            if (req->content_len > BACKGROUND_MAX_BYTES) return reject(req, "413 Content Too Large", "background exceeds 196608 bytes");
            if (httpd_req_get_hdr_value_str(req, "Content-Type", text, sizeof(text)) != ESP_OK || strcasecmp(text, "image/jpeg"))
                return reject(req, "415 Unsupported Media Type", "Content-Type must be image/jpeg");
            if (!req->content_len || !httpd_req_get_hdr_value_len(req, "Content-Length") || httpd_req_get_hdr_value_len(req, "Transfer-Encoding"))
                return reject(req, "422 Unprocessable Content", "nonempty Content-Length body required");
            if (httpd_req_get_hdr_value_str(req, "X-Content-SHA256", text, sizeof(text)) != ESP_OK || !background_parse_sha256(text, job.sha))
                return reject(req, "422 Unprocessable Content", "valid X-Content-SHA256 required");
        } else if (req->content_len) return reject(req, "422 Unprocessable Content", "DELETE must not contain a body");
    }
    if (!claim()) return reject(req, "409 Conflict", "background worker busy");
    if (httpd_req_async_handler_begin(req, &job.req) != ESP_OK) { release(); return reject(req, "503 Service Unavailable", "async request allocation failed"); }
    if (xQueueSend(s_queue, &job, 0) != pdTRUE) {
        failure(job.req, "503 Service Unavailable", "background queue unavailable");
        shutdown(httpd_req_to_sockfd(job.req), SHUT_RDWR);
        httpd_req_async_handler_complete(job.req);
        release();
    }
    return ESP_OK;
}
static esp_err_t image_get(httpd_req_t *req)
{
    if (req->content_len) return reject(req, "422 Unprocessable Content", "GET must not contain a body");
    return submit(req, JOB_IMAGE);
}
static esp_err_t image_put(httpd_req_t *req) { return submit(req, JOB_PUT); }
static esp_err_t image_delete(httpd_req_t *req) { return submit(req, JOB_DELETE); }
esp_err_t background_http_register(httpd_handle_t server)
{
    s_queue = xQueueCreate(1, sizeof(job_t));
    if (s_queue && xTaskCreate(worker, "background", 8192, NULL, 2, NULL) != pdPASS) { vQueueDelete(s_queue); s_queue = NULL; }
    if (!s_queue) ESP_LOGE(TAG, "background worker unavailable (status route remains available)");
    const httpd_uri_t routes[] = {
        {.uri = "/api/background", .method = HTTP_GET, .handler = status_get},
        {.uri = "/api/background/image", .method = HTTP_GET, .handler = image_get},
        {.uri = "/api/background", .method = HTTP_PUT, .handler = image_put},
        {.uri = "/api/background", .method = HTTP_DELETE, .handler = image_delete},
    };
    esp_err_t result = ESP_OK;
    for (unsigned i = 0; i < sizeof(routes) / sizeof(routes[0]); ++i) {
        esp_err_t err = httpd_register_uri_handler(server, &routes[i]);
        if (err != ESP_OK) { ESP_LOGE(TAG, "register %s failed: %s", routes[i].uri, esp_err_to_name(err)); result = err; }
    }
    return result;
}
