// HTTP server: Wi-Fi setup page (captive portal + reconfig via aemeath.local) and /ws for the agent.
#include <stdlib.h>
#include <string.h>

#include "cJSON.h"
#include "display.h"
#include "esp_http_server.h"
#include "esp_log.h"
#include "esp_wifi.h"
#include "metrics.h"
#include "net.h"
#include "voice_ws.h"
#include "background_http.h"

static const char *TAG = "server";

#define WS_MAX_FRAME 8192
#define WS_ALL_CLIENTS -1

static httpd_handle_t s_srv;

static const char PAGE[] =
    "<!doctype html><html><head><meta charset=utf-8>"
    "<meta name=viewport content='width=device-width,initial-scale=1'>"
    "<title>Aemeath Wi-Fi</title><style>"
    "body{font-family:-apple-system,system-ui,sans-serif;background:#0d1117;color:#e6edf3;margin:0;padding:24px}"
    ".c{max-width:420px;margin:auto}h1{font-size:22px}label{display:block;margin:14px 0 6px;color:#8b949e}"
    "select,input,button{width:100%;box-sizing:border-box;padding:12px;border-radius:8px;border:1px solid #30363d;"
    "background:#161b22;color:#e6edf3;font-size:16px}button{background:#238636;border:0;margin-top:20px;font-weight:600}"
    "small{color:#8b949e}</style></head><body><div class=c>"
    "<h1>Aemeath 性能监控台 · Wi-Fi 设置</h1>"
    "<form method=post action=/save>"
    "<label>网络</label><select id=s onchange=\"q.value=this.value\"><option value=''>扫描中…</option></select>"
    "<label>SSID</label><input id=q name=ssid required maxlength=32>"
    "<label>密码</label><input name=pass type=password maxlength=64>"
    "<button>保存并重启</button></form>"
    "<p><small>保存后设备将连接到该网络，并以 aemeath.local 广播。</small></p></div><script>"
    "fetch('/scan').then(r=>r.json()).then(l=>{let s=document.getElementById('s');"
    "s.innerHTML='<option value=\"\">选择网络…</option>';"
    "l.forEach(a=>{let o=document.createElement('option');o.value=a.ssid;"
    "o.textContent=a.ssid+'  ('+a.rssi+' dBm'+(a.open?', 开放':'')+')';s.appendChild(o)})})"
    "</script></body></html>";

static const char SAVED[] =
    "<!doctype html><html><head><meta charset=utf-8><meta name=viewport content='width=device-width'></head>"
    "<body style='font-family:system-ui;background:#0d1117;color:#e6edf3;padding:24px'>"
    "<h2>已保存，设备正在重启…</h2><p>请将电脑/手机切回原 Wi-Fi。</p></body></html>";

static esp_err_t root_get(httpd_req_t *req)
{
    httpd_resp_set_type(req, "text/html; charset=utf-8");
    return httpd_resp_send(req, PAGE, sizeof(PAGE) - 1);
}

static esp_err_t scan_get(httpd_req_t *req)
{
    wifi_mode_t mode;
    esp_wifi_get_mode(&mode);
    if (mode == WIFI_MODE_AP) {
        esp_wifi_set_mode(WIFI_MODE_APSTA);  // scanning needs the STA interface
    }
    uint16_t n = 20;
    wifi_ap_record_t *recs = calloc(n, sizeof(*recs));
    cJSON *arr = cJSON_CreateArray();
    if (recs && esp_wifi_scan_start(NULL, true) == ESP_OK && esp_wifi_scan_get_ap_records(&n, recs) == ESP_OK) {
        for (int i = 0; i < n; i++) {
            if (!recs[i].ssid[0]) continue;
            bool dup = false;
            for (int j = 0; j < i; j++) {
                dup |= strcmp((char *)recs[i].ssid, (char *)recs[j].ssid) == 0;
            }
            if (dup) continue;
            cJSON *o = cJSON_CreateObject();
            cJSON_AddStringToObject(o, "ssid", (char *)recs[i].ssid);
            cJSON_AddNumberToObject(o, "rssi", recs[i].rssi);
            cJSON_AddBoolToObject(o, "open", recs[i].authmode == WIFI_AUTH_OPEN);
            cJSON_AddItemToArray(arr, o);
        }
    }
    free(recs);
    char *out = cJSON_PrintUnformatted(arr);
    cJSON_Delete(arr);
    httpd_resp_set_type(req, "application/json");
    esp_err_t err = httpd_resp_sendstr(req, out ? out : "[]");
    free(out);
    return err;
}

static void url_decode(char *s)
{
    char *o = s;
    for (; *s; s++) {
        if (*s == '+') {
            *o++ = ' ';
        } else if (*s == '%' && s[1] && s[2]) {
            char hex[3] = {s[1], s[2], 0};
            *o++ = (char)strtol(hex, NULL, 16);
            s += 2;
        } else {
            *o++ = *s;
        }
    }
    *o = 0;
}

static esp_err_t save_post(httpd_req_t *req)
{
    char body[256];
    int len = req->content_len < sizeof(body) - 1 ? req->content_len : sizeof(body) - 1;
    int got = 0;
    while (got < len) {
        int r = httpd_req_recv(req, body + got, len - got);
        if (r <= 0) {
            return ESP_FAIL;
        }
        got += r;
    }
    body[got] = 0;

    char ssid[33] = {0}, pass[65] = {0};
    char enc[200];
    if (httpd_query_key_value(body, "ssid", enc, sizeof(enc)) == ESP_OK) {
        url_decode(enc);
        strlcpy(ssid, enc, sizeof(ssid));
    }
    if (httpd_query_key_value(body, "pass", enc, sizeof(enc)) == ESP_OK) {
        url_decode(enc);
        strlcpy(pass, enc, sizeof(pass));
    }
    if (!ssid[0]) {
        return httpd_resp_send_err(req, HTTPD_400_BAD_REQUEST, "ssid required");
    }
    httpd_resp_set_type(req, "text/html; charset=utf-8");
    httpd_resp_send(req, SAVED, sizeof(SAVED) - 1);
    net_save_credentials(ssid, pass);
    return ESP_OK;
}

// Runs on the httpd task (via httpd_queue_work). fd < 0 means every WS client.
static void screen_state_send(void *arg)
{
    int target = (int)(intptr_t)arg;
    bool on;
    int brightness;
    display_get_screen(&on, &brightness);
    char msg[80];
    int len = snprintf(msg, sizeof(msg), "{\"v\":1,\"type\":\"screen_state\",\"on\":%s,\"brightness\":%d}",
                       on ? "true" : "false", brightness);
    httpd_ws_frame_t frame = {.type = HTTPD_WS_TYPE_TEXT, .payload = (uint8_t *)msg, .len = len, .final = true};

    int fds[CONFIG_LWIP_MAX_SOCKETS];
    size_t n = sizeof(fds) / sizeof(fds[0]);
    if (target >= 0) {
        fds[0] = target;
        n = 1;
    } else if (httpd_get_client_list(s_srv, &n, fds) != ESP_OK) {
        return;
    }
    for (size_t i = 0; i < n; i++) {
        if (!voice_ws_is_client(fds[i]) && httpd_ws_get_fd_info(s_srv, fds[i]) == HTTPD_WS_CLIENT_WEBSOCKET) {
            httpd_ws_send_frame_async(s_srv, fds[i], &frame);
        }
    }
}

void server_broadcast_screen(void)
{
    if (s_srv) {
        httpd_queue_work(s_srv, screen_state_send, (void *)(intptr_t)WS_ALL_CLIENTS);
    }
}

// IDF v6 no longer calls the WS handler for the handshake GET; this runs right
// after the handshake response instead.
static esp_err_t ws_post_handshake(httpd_req_t *req)
{
    int fd = httpd_req_to_sockfd(req);
    ESP_LOGI(TAG, "agent connected (fd %d)", fd);
    // The device owns screen state: tell the new agent what it is.
    httpd_queue_work(req->handle, screen_state_send, (void *)(intptr_t)fd);
    return ESP_OK;
}

static esp_err_t ws_handler(httpd_req_t *req)
{
    if (req->method == HTTP_GET) {
        return ESP_OK;
    }
    httpd_ws_frame_t frame = {0};
    esp_err_t err = httpd_ws_recv_frame(req, &frame, 0);
    if (err != ESP_OK) {
        return err;
    }
    if (frame.type != HTTPD_WS_TYPE_TEXT || frame.len == 0 || frame.len > WS_MAX_FRAME) {
        return ESP_OK;
    }
    uint8_t *buf = malloc(frame.len + 1);
    if (!buf) {
        return ESP_ERR_NO_MEM;
    }
    frame.payload = buf;
    err = httpd_ws_recv_frame(req, &frame, frame.len);
    if (err == ESP_OK) {
        buf[frame.len] = 0;
        if (!metrics_ingest((char *)buf, frame.len)) {
            ESP_LOGW(TAG, "bad frame");
        }
    }
    free(buf);
    return err;
}

// Anything unknown (OS captive-portal probes etc.) goes to the setup page.
static esp_err_t redirect_404(httpd_req_t *req, httpd_err_code_t code)
{
    httpd_resp_set_status(req, "302 Found");
    httpd_resp_set_hdr(req, "Location", "http://" NET_AP_IP "/");
    return httpd_resp_send(req, NULL, 0);
}

void server_start(void)
{
    httpd_config_t cfg = HTTPD_DEFAULT_CONFIG();
    cfg.stack_size = 8192;
    cfg.max_uri_handlers = 16;
    cfg.max_open_sockets = 7;
    cfg.lru_purge_enable = true;
    cfg.send_wait_timeout = 1;
    cfg.recv_wait_timeout = 1;
    httpd_handle_t srv;
    ESP_ERROR_CHECK(httpd_start(&srv, &cfg));
    s_srv = srv;
    // An optional voice allocation must never take down metrics or provisioning.
    esp_err_t voice_err = voice_ws_register(srv);
    if (voice_err != ESP_OK) ESP_LOGE(TAG, "voice service unavailable: %s", esp_err_to_name(voice_err));

    const httpd_uri_t uris[] = {
        {.uri = "/", .method = HTTP_GET, .handler = root_get},
        {.uri = "/scan", .method = HTTP_GET, .handler = scan_get},
        {.uri = "/save", .method = HTTP_POST, .handler = save_post},
        {.uri = "/ws", .method = HTTP_GET, .handler = ws_handler, .is_websocket = true,
         .ws_post_handshake_cb = ws_post_handshake},
    };
    for (int i = 0; i < sizeof(uris) / sizeof(uris[0]); i++) {
        ESP_ERROR_CHECK(httpd_register_uri_handler(srv, &uris[i]));
    }
    esp_err_t background_err = background_http_register(srv);
    if (background_err != ESP_OK) ESP_LOGE(TAG, "background routes unavailable: %s", esp_err_to_name(background_err));
    ESP_ERROR_CHECK(httpd_register_err_handler(srv, HTTPD_404_NOT_FOUND, redirect_404));
}
