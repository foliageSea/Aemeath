// Actual production codec/runtime/HTTP, with only chip/OS/server boundaries
// stubbed. The chip JPEG library is not executable on the host: its API contract
// and failure cleanup are checked here; real software rendering is a separate test.
#define _POSIX_C_SOURCE 200809L
#define _DARWIN_C_SOURCE
#include "background.h"
#include "background_format.h"
#include "esp_jpeg_dec.h"
#include <assert.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

#define ESP_OK 0
#define ESP_FAIL -1
#define ESP_ERR_TIMEOUT -2
#define ESP_ERR_INVALID_ARG -3
#define ESP_LOGE(tag, ...) do { (void)(tag); if (0) fprintf(stderr, __VA_ARGS__); } while (0)
#define ESP_LOGW(tag, ...) ESP_LOGE(tag, __VA_ARGS__)
#define ESP_LOGI(tag, ...) ESP_LOGE(tag, __VA_ARGS__)
#define MALLOC_CAP_SPIRAM 1
#define MALLOC_CAP_8BIT 2
#define portMUX_INITIALIZER_UNLOCKED 0
#define portENTER_CRITICAL(p) ((void)(p))
#define portEXIT_CRITICAL(p) ((void)(p))
#define portMAX_DELAY UINT32_MAX
#define pdTRUE 1
#define pdPASS 1
#define ESP_PARTITION_TYPE_DATA 1
#define ESP_PARTITION_SUBTYPE_ANY 0xff
#define HTTP_GET 1
#define HTTP_PUT 2
#define HTTP_DELETE 3
#define HTTPD_SOCK_ERR_FAIL -1
#define HTTPD_SOCK_ERR_TIMEOUT -3

typedef int esp_err_t;
typedef int portMUX_TYPE;
static const char *esp_err_to_name(int err) { (void)err; return "host error"; }
static int64_t clock_offset;
static int64_t esp_timer_get_time(void) { struct timespec t; clock_gettime(CLOCK_MONOTONIC, &t); return (int64_t)t.tv_sec * 1000000 + t.tv_nsec / 1000 + clock_offset; }
static void vTaskDelay(unsigned ticks) { (void)ticks; }
static const uint8_t *display_pixels;
static bool swap_fails;
static bool dashboard_set_background(const uint8_t *pixels, uint32_t timeout)
{
    assert(timeout > 0);
    if (swap_fails) return false;
    display_pixels = pixels; return true;
}
static void *tracked[128];
static unsigned live_allocs, alloc_calls, fail_alloc_at;
static void *track(void *p) { if (p) { assert(live_allocs < 128); tracked[live_allocs++] = p; } return p; }
static void *heap_caps_malloc(size_t n, unsigned caps)
{
    assert(caps == (MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT));
    if (++alloc_calls == fail_alloc_at) return NULL;
    return track(malloc(n));
}
static void *heap_caps_aligned_alloc(size_t align, size_t n, unsigned caps)
{
    assert(align == 16 && caps == (MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT));
    if (++alloc_calls == fail_alloc_at) return NULL;
    void *p = NULL; assert(posix_memalign(&p, align, n) == 0); return track(p);
}
static bool fail_transport_alloc;
static void *host_malloc(size_t n) { return fail_transport_alloc ? NULL : malloc(n); }
static void host_free(void *p)
{
    assert(!p || p != display_pixels);
    for (unsigned i = 0; i < live_allocs; ++i) if (tracked[i] == p) { tracked[i] = tracked[--live_allocs]; break; }
    free(p);
}

static bool partition_exists = true, flash_fails;
static int read_fail_slot = -1;
static unsigned writes, erases;
static uint8_t flash_bytes[BACKGROUND_PARTITION_BYTES];
typedef struct { unsigned address, size; } esp_partition_t;
static const esp_partition_t partition = {BACKGROUND_PARTITION_OFFSET, BACKGROUND_PARTITION_BYTES};
static const esp_partition_t *esp_partition_find_first(int type, int sub, const char *name)
{ assert(type == ESP_PARTITION_TYPE_DATA && sub == ESP_PARTITION_SUBTYPE_ANY && !strcmp(name, "background")); return partition_exists ? &partition : NULL; }
static esp_err_t esp_partition_read(const esp_partition_t *p, size_t at, void *out, size_t len)
{ assert(p == &partition && at <= sizeof(flash_bytes) && len <= sizeof(flash_bytes) - at); if (flash_fails || (int)(at / BACKGROUND_SLOT_BYTES) == read_fail_slot) return ESP_FAIL; memcpy(out, flash_bytes + at, len); return ESP_OK; }
static esp_err_t esp_partition_write(const esp_partition_t *p, size_t at, const void *data, size_t len)
{ assert(p == &partition && at <= sizeof(flash_bytes) && len <= sizeof(flash_bytes) - at); if (flash_fails) return ESP_FAIL; const uint8_t *s = data; for (size_t i = 0; i < len; ++i) { assert((flash_bytes[at + i] & s[i]) == s[i]); flash_bytes[at + i] &= s[i]; } ++writes; return ESP_OK; }
static esp_err_t esp_partition_erase_range(const esp_partition_t *p, size_t at, size_t len)
{ assert(p == &partition && at % 4096 == 0 && len == 4096 && at + len <= sizeof(flash_bytes)); if (flash_fails) return ESP_FAIL; memset(flash_bytes + at, 0xff, len); ++erases; return ESP_OK; }

#define PSA_SUCCESS 0
#define PSA_ALG_SHA_256 1
#define PSA_HASH_OPERATION_INIT {0}
typedef struct { uint32_t hash; } psa_hash_operation_t;
static int psa_crypto_init(void) { return PSA_SUCCESS; }
static int psa_hash_setup(psa_hash_operation_t *op, int algorithm) { assert(algorithm == PSA_ALG_SHA_256); op->hash = 2166136261u; return PSA_SUCCESS; }
static int psa_hash_update(psa_hash_operation_t *op, const uint8_t *p, size_t n) { for (size_t i = 0; i < n; ++i) op->hash = (op->hash ^ p[i]) * 16777619u; return PSA_SUCCESS; }
static int psa_hash_finish(psa_hash_operation_t *op, uint8_t *out, size_t capacity, size_t *actual)
{ assert(capacity == 32); uint32_t h = op->hash; for (unsigned i = 0; i < 32; ++i) { h = h * 1664525u + 1013904223u; out[i] = (uint8_t)(h >> 24); } *actual = 32; return PSA_SUCCESS; }
static int psa_hash_abort(psa_hash_operation_t *op) { (void)op; return PSA_SUCCESS; }
static int psa_hash_compute(int algorithm, const uint8_t *p, size_t n, uint8_t *out, size_t cap, size_t *actual)
{ psa_hash_operation_t op; psa_hash_setup(&op, algorithm); psa_hash_update(&op, p, n); return psa_hash_finish(&op, out, cap, actual); }

static jpeg_error_t jpeg_open_result, jpeg_parse_result, jpeg_process_result;
static int jpeg_width = 480, jpeg_output_bytes = 460800;
static unsigned decoder_opens, decoder_closes, decoder_processes;
jpeg_error_t jpeg_dec_open(jpeg_dec_config_t *config, jpeg_dec_handle_t *out)
{ assert(config->output_type == JPEG_PIXEL_FORMAT_RGB565_LE && !config->block_enable); if (jpeg_open_result) return jpeg_open_result; *out = malloc(1); assert(*out); ++decoder_opens; return JPEG_ERR_OK; }
jpeg_error_t jpeg_dec_parse_header(jpeg_dec_handle_t d, jpeg_dec_io_t *io, jpeg_dec_header_info_t *out)
{ assert(d && io->inbuf && io->inbuf_len > 0); out->width = jpeg_width; out->height = 480; return jpeg_parse_result; }
jpeg_error_t jpeg_dec_get_outbuf_len(jpeg_dec_handle_t d, int *n) { assert(d); *n = jpeg_output_bytes; return JPEG_ERR_OK; }
jpeg_error_t jpeg_dec_process(jpeg_dec_handle_t d, jpeg_dec_io_t *io)
{ assert(d && (uintptr_t)io->outbuf % 16 == 0 && io->out_size == 460800); memset(io->outbuf, 0x5a, io->out_size); ++decoder_processes; return jpeg_process_result; }
jpeg_error_t jpeg_dec_close(jpeg_dec_handle_t d) { free(d); ++decoder_closes; return JPEG_ERR_OK; }

// HTTPD/queue boundaries keep original response storage alive after async copy.
typedef void *httpd_handle_t;
typedef void (*httpd_free_ctx_fn_t)(void *);
typedef int (*httpd_send_func_t)(httpd_handle_t, int, const char *, size_t, int);
typedef int (*httpd_recv_func_t)(httpd_handle_t, int, char *, size_t, int);
typedef struct { int code; char type[64], etag[32], body[BACKGROUND_MAX_BYTES + 1024]; size_t length; unsigned completed, sends, fail_send_at; } response_t;
typedef struct { int fd; void *transport; httpd_free_ctx_fn_t transport_free; httpd_send_func_t send; httpd_recv_func_t recv; } session_t;
typedef struct {
    size_t content_len;
    httpd_handle_t handle;
    const char *if_match, *content_type, *sha, *length_header, *transfer_encoding;
    const uint8_t *input; size_t input_length, input_pos;
    int recv_error;
    bool asynchronous;
    session_t *session;
    response_t *response;
} httpd_req_t;
typedef struct { const char *uri; int method; esp_err_t (*handler)(httpd_req_t *); } httpd_uri_t;
static int registered, fail_register_at;
static esp_err_t httpd_register_uri_handler(httpd_handle_t hd, const httpd_uri_t *route)
{ (void)hd; assert(route->uri && route->handler); return ++registered == fail_register_at ? ESP_FAIL : ESP_OK; }
static const char *header(httpd_req_t *req, const char *name)
{
    if (!strcmp(name, "If-Match")) return req->if_match;
    if (!strcmp(name, "Content-Type")) return req->content_type;
    if (!strcmp(name, "X-Content-SHA256")) return req->sha;
    if (!strcmp(name, "Content-Length")) return req->length_header;
    if (!strcmp(name, "Transfer-Encoding")) return req->transfer_encoding;
    return NULL;
}
static size_t httpd_req_get_hdr_value_len(httpd_req_t *req, const char *name) { const char *p = header(req, name); return p ? strlen(p) : 0; }
static esp_err_t httpd_req_get_hdr_value_str(httpd_req_t *req, const char *name, char *out, size_t cap)
{ const char *p = header(req, name); if (!p || strlen(p) >= cap) return ESP_FAIL; strcpy(out, p); return ESP_OK; }
static esp_err_t httpd_resp_set_status(httpd_req_t *req, const char *status) { req->response->code = atoi(status); return ESP_OK; }
static esp_err_t httpd_resp_set_type(httpd_req_t *req, const char *type) { snprintf(req->response->type, sizeof(req->response->type), "%s", type); return ESP_OK; }
static esp_err_t httpd_resp_set_hdr(httpd_req_t *req, const char *name, const char *value)
{ if (!strcmp(name, "ETag")) snprintf(req->response->etag, sizeof(req->response->etag), "%s", value); return ESP_OK; }
static esp_err_t httpd_resp_send(httpd_req_t *req, const char *data, size_t len)
{
    response_t *r = req->response;
    if (++r->sends == r->fail_send_at) return ESP_FAIL;
    assert(r->length + len < sizeof(r->body));
    if (len) memcpy(r->body + r->length, data, len);
    r->length += len; r->body[r->length] = 0;
    if (!r->code) r->code = 200;
    return ESP_OK;
}
static esp_err_t httpd_resp_sendstr(httpd_req_t *req, const char *body) { return httpd_resp_send(req, body, strlen(body)); }
static esp_err_t httpd_resp_send_chunk(httpd_req_t *req, const char *body, size_t len) { return httpd_resp_send(req, body, len); }
static int httpd_req_recv(httpd_req_t *req, char *out, size_t len)
{ if (req->recv_error) return req->recv_error; if (req->input_pos == req->input_length) return 0; size_t n = req->input_length - req->input_pos; if (n > len) n = len; if (n > 7) n = 7; memcpy(out, req->input + req->input_pos, n); req->input_pos += n; return (int)n; }
static bool begin_fails, queue_fails;
static unsigned async_started, async_completed;
static esp_err_t httpd_req_async_handler_begin(httpd_req_t *req, httpd_req_t **copy)
{ if (begin_fails) return ESP_FAIL; *copy = malloc(sizeof(**copy)); assert(*copy); **copy = *req; (*copy)->asynchronous = true; ++async_started; return ESP_OK; }
static esp_err_t httpd_req_async_handler_complete(httpd_req_t *req)
{ assert(req->asynchronous); ++req->response->completed; ++async_completed; free(req); return ESP_OK; }
static int httpd_req_to_sockfd(httpd_req_t *req) { return req->session->fd; }
static void *httpd_sess_get_transport_ctx(httpd_handle_t hd, int fd) { session_t *s = hd; assert(s->fd == fd); return s->transport; }
static void httpd_sess_set_transport_ctx(httpd_handle_t hd, int fd, void *ctx, httpd_free_ctx_fn_t cleanup)
{ session_t *s = hd; assert(s->fd == fd && !s->transport); s->transport = ctx; s->transport_free = cleanup; }
static esp_err_t httpd_sess_set_send_override(httpd_handle_t hd, int fd, httpd_send_func_t fn) { session_t *s = hd; assert(s->fd == fd); s->send = fn; return ESP_OK; }
static esp_err_t httpd_sess_set_recv_override(httpd_handle_t hd, int fd, httpd_recv_func_t fn) { session_t *s = hd; assert(s->fd == fd); s->recv = fn; return ESP_OK; }
typedef struct { size_t size; bool pending; uint8_t bytes[256]; } *QueueHandle_t;
static QueueHandle_t xQueueCreate(unsigned n, size_t size) { assert(n == 1 && size <= 256); QueueHandle_t q = calloc(1, sizeof(*q)); assert(q); q->size = size; return q; }
static void vQueueDelete(QueueHandle_t q) { free(q); }
static int xQueueSend(QueueHandle_t q, const void *data, unsigned timeout)
{ (void)timeout; if (queue_fails || q->pending) return 0; memcpy(q->bytes, data, q->size); q->pending = true; return pdTRUE; }
static int xQueueReceive(QueueHandle_t q, void *data, unsigned timeout)
{ (void)timeout; if (!q->pending) return 0; memcpy(data, q->bytes, q->size); q->pending = false; return pdTRUE; }
static int xTaskCreate(void (*fn)(void *), const char *name, unsigned stack, void *arg, unsigned priority, void *handle)
{ (void)fn; (void)name; (void)stack; (void)arg; (void)priority; (void)handle; return pdPASS; }

#define free host_free
#define malloc host_malloc
#include "background_codec.c"
#define TAG runtime_TAG
#include "background.c"
#undef TAG
#define TAG http_TAG
#include "net/background_http.c"
#undef TAG
#undef free
#undef malloc

static uint8_t jpeg[] = {0xff,0xd8,0xff,0xc0,0,11,8,1,0xe0,1,0xe0,1,1,0x11,0,
                        0xff,0xda,0,8,1,1,0,0,63,0,0x18,0xff,0,0x30,0xff,0xd0,0xff,0xd9};
static uint8_t jpeg_sha[32];
static char sha_text[65];
static void reset_runtime(bool supported)
{
    display_pixels = NULL;
    host_free(s_pixels); s_pixels = NULL;
    memset(&s_store, 0, sizeof(s_store)); memset(&s_status, 0, sizeof(s_status));
    s_partition = NULL; s_deadline = 0;
    memset(flash_bytes, 0xff, sizeof(flash_bytes)); partition_exists = supported;
    swap_fails = flash_fails = false; read_fail_slot = -1; writes = erases = alloc_calls = fail_alloc_at = 0;
    jpeg_open_result = jpeg_parse_result = jpeg_process_result = JPEG_ERR_OK;
    jpeg_width = 480; jpeg_output_bytes = 460800;
    background_init();
    assert(!live_allocs);
}
static void runtime_checks(void)
{
    reset_runtime(false); background_status_t status; background_snapshot(&status);
    assert(!status.supported && !status.present && status.error[0]);
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_UNSUPPORTED);
    reset_runtime(true);
    background_snapshot(&status); assert(status.supported && !status.present && !status.displayed && !status.revision);
    assert(background_put(1, jpeg, sizeof(jpeg), jpeg_sha) == BG_CONFLICT);
    uint8_t wrong[32] = {0}; assert(background_put(0, jpeg, sizeof(jpeg), wrong) == BG_INVALID && !writes);
    fail_alloc_at = alloc_calls + 1;
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_NO_MEMORY && !writes && !live_allocs);
    fail_alloc_at = 0; jpeg_process_result = JPEG_ERR_BAD_DATA;
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_INVALID && !writes && !live_allocs);
    jpeg_process_result = JPEG_ERR_OK; jpeg_width = 481;
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_INVALID && !writes && !live_allocs);
    jpeg_width = 480; jpeg_output_bytes = 460802;
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_INVALID && !writes && !live_allocs);
    jpeg_output_bytes = 460800;
    background_set_deadline(esp_timer_get_time() - 1);
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_TIMEOUT && !writes);
    background_set_deadline(0);
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_OK && live_allocs == 1 && display_pixels);
    unsigned before = writes;
    assert(background_put(1, jpeg, sizeof(jpeg), jpeg_sha) == BG_OK && writes == before);
    // Simulate app restart: preserve flash and restore only a final JPEG.
    display_pixels = NULL; host_free(s_pixels); s_pixels = NULL;
    background_init(); assert(live_allocs == 1 && display_pixels);
    swap_fails = true;
    assert(background_delete(1) == BG_IO);
    background_snapshot(&status);
    assert(!status.present && !status.displayed && status.revision == 2 && status.error[0] && display_pixels);
    assert(!status.sha256[0] && !status.bytes && !status.width && !status.height);
    before = writes; swap_fails = false;
    assert(background_delete(2) == BG_OK && writes == before && !display_pixels && !live_allocs);
    background_snapshot(&status); assert(!status.error[0]);
    background_init(); assert(!display_pixels && !s_status.present && s_status.revision == 2);
    // One unreadable slot can contain a newer committed image. Do not mistake
    // a valid older slot for the active record and erase the unknown slot.
    reset_runtime(true);
    assert(background_put(0, jpeg, sizeof(jpeg), jpeg_sha) == BG_OK);
    uint8_t second[sizeof(jpeg)], second_sha[32];
    memcpy(second, jpeg, sizeof(second)); second[25] ^= 1;
    assert(background_sha256(second, sizeof(second), second_sha));
    assert(background_put(1, second, sizeof(second), second_sha) == BG_OK);
    display_pixels = NULL; host_free(s_pixels); s_pixels = NULL;
    before = writes; read_fail_slot = 1;
    background_init(); background_snapshot(&status);
    assert(!s_ready && !display_pixels && !status.displayed && status.error[0]);
    assert(status.revision == 1); // provisional older record, explicitly not writable
    assert(background_put(1, jpeg, sizeof(jpeg), jpeg_sha) == BG_IO);
    assert(background_delete(1) == BG_IO && writes == before);
    read_fail_slot = -1;
    background_init(); background_snapshot(&status);
    assert(s_ready && status.revision == 2 && status.displayed && !status.error[0]);
    assert(background_delete(2) == BG_OK && !live_allocs);
    assert(decoder_opens == decoder_closes);
    puts("PASS: unknown-slot scan IO fails closed; successful fresh scan restores newest record");
    puts("PASS: codec alignment/size/error cleanup, runtime restore, durable deletion/retry, no premature source free");
}
static void run_pending(void) { job_t job; assert(xQueueReceive(s_queue, &job, 0)); process_job(&job); release(); }
static void fixture(httpd_req_t *req, response_t *response, session_t *session, int pair[2])
{
    memset(response, 0, sizeof(*response)); assert(socketpair(AF_UNIX, SOCK_STREAM, 0, pair) == 0);
    *session = (session_t){.fd = pair[0]};
    *req = (httpd_req_t){.content_len = sizeof(jpeg), .handle = session, .session = session, .response = response,
        .if_match = "\"0\"", .content_type = "image/jpeg", .sha = sha_text, .length_header = "33",
        .input = jpeg, .input_length = sizeof(jpeg)};
}
static void dispose(session_t *s, int pair[2])
{
    if (s->transport && s->transport_free) s->transport_free(s->transport);
    close(pair[0]); close(pair[1]);
}
static response_t response;
static void http_checks(void)
{
    reset_runtime(true); assert(background_http_register(NULL) == ESP_OK && registered == 4);
    httpd_req_t req; session_t session; int pair[2];
    fixture(&req, &response, &session, pair);
    req.content_len = 0;
    s_status.revision = UINT64_MAX;
    assert(status_get(&req) == ESP_OK && strstr(response.body, "18446744073709551615") && !strcmp(response.etag, "\"18446744073709551615\""));
    s_status.revision = 0;
    dispose(&session, pair);
    const int codes[] = {412,413,415,422,422};
    for (unsigned i = 0; i < 5; ++i) {
        fixture(&req, &response, &session, pair);
        if (i == 0) req.if_match = "\"18446744073709551616\"";
        if (i == 1) req.content_len = BACKGROUND_MAX_BYTES + 1;
        if (i == 2) req.content_type = "image/png";
        if (i == 3) req.sha = "bad";
        if (i == 4) req.content_len = 0;
        assert(image_put(&req) == ESP_FAIL && response.code == codes[i] && !async_started);
        dispose(&session, pair);
    }
    fixture(&req, &response, &session, pair);
    begin_fails = true; assert(image_put(&req) == ESP_FAIL && response.code == 503 && !s_busy); begin_fails = false;
    dispose(&session, pair);
    fixture(&req, &response, &session, pair);
    queue_fails = true; assert(image_put(&req) == ESP_OK && response.code == 503 && response.completed == 1 && !s_busy); queue_fails = false;
    dispose(&session, pair);
    fixture(&req, &response, &session, pair);
    assert(image_put(&req) == ESP_OK && s_busy && !response.sends); // handler did not receive/decode/write
    response_t other_response; httpd_req_t other; session_t other_session; int other_pair[2];
    fixture(&other, &other_response, &other_session, other_pair);
    assert(image_put(&other) == ESP_FAIL && other_response.code == 409);
    dispose(&other_session, other_pair);
    run_pending(); assert(response.code == 200 && response.completed == 1 && !strcmp(response.etag, "\"1\""));
    assert(strstr(response.body, "\"present\":true") && strstr(response.body, "\"displayed\":true") && !s_busy);
    dispose(&session, pair);
    // Reading image honors optional current If-Match; does all big sends on worker.
    fixture(&req, &response, &session, pair);
    req.content_len = 0;
    assert(image_get(&req) == ESP_FAIL && response.code == 412); dispose(&session, pair);
    fixture(&req, &response, &session, pair); req.if_match = "\"1\"";
    req.content_len = 0;
    assert(image_get(&req) == ESP_OK && !response.sends); run_pending();
    assert(response.code == 200 && response.length == sizeof(jpeg) && !memcmp(response.body, jpeg, sizeof(jpeg)) && response.completed == 1);
    dispose(&session, pair);
    // Decoder/digest/body/transport failures complete async requests exactly once.
    for (unsigned i = 0; i < 5; ++i) {
        fixture(&req, &response, &session, pair); req.if_match = "\"1\"";
        if (i == 0) req.input_length = 5;
        if (i == 1) req.recv_error = HTTPD_SOCK_ERR_TIMEOUT;
        if (i == 2) fail_alloc_at = alloc_calls + 1;
        if (i == 3) req.sha = "0000000000000000000000000000000000000000000000000000000000000000";
        if (i == 4) fail_transport_alloc = true;
        assert(image_put(&req) == ESP_OK); run_pending();
        assert(response.code == (i == 1 ? 408 : i == 2 || i == 4 ? 503 : 422) && response.completed == 1 && !s_busy);
        fail_alloc_at = 0; fail_transport_alloc = false; dispose(&session, pair);
    }
    fixture(&req, &response, &session, pair); req.if_match = "\"1\""; req.content_len = 0;
    response.fail_send_at = 1; // commit succeeded but the response was lost
    assert(image_delete(&req) == ESP_OK); run_pending();
    assert(response.completed == 1 && s_status.revision == 2 && !s_status.present && !s_status.error[0]);
    dispose(&session, pair);
    fixture(&req, &response, &session, pair); req.if_match = "\"2\"";
    req.content_len = 0;
    assert(image_get(&req) == ESP_OK); run_pending(); assert(response.code == 404 && response.completed == 1); dispose(&session, pair);
    fixture(&req, &response, &session, pair);
    transport_t deadline = {.deadline = esp_timer_get_time() - 1}; session.transport = &deadline;
    char byte;
    assert(deadline_send(&session, pair[0], "x", 1, 0) == HTTPD_SOCK_ERR_TIMEOUT);
    assert(deadline_recv(&session, pair[0], &byte, 1, 0) == HTTPD_SOCK_ERR_TIMEOUT);
    session.transport = NULL; dispose(&session, pair);
    assert(async_started == async_completed && !live_allocs);
    reset_runtime(false); fixture(&req, &response, &session, pair);
    req.content_len = 0;
    assert(status_get(&req) == ESP_OK && response.code == 200 && strstr(response.body, "\"supported\":false"));
    dispose(&session, pair);
    vQueueDelete(s_queue); s_queue = NULL;
    printf("PASS: exact uint64 JSON, API status codes, async body/send ownership, busy, lost response, %u complete/%u begin\n", async_completed, async_started);
}
int main(void)
{
    background_sha256(jpeg, sizeof(jpeg), jpeg_sha); background_hex_sha256(jpeg_sha, sha_text);
    runtime_checks(); http_checks(); return 0;
}
