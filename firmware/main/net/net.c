#include "net.h"

#include <string.h>

#include "esp_event.h"
#include "esp_log.h"
#include "esp_mac.h"
#include "esp_netif.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "mdns.h"
#include "nvs.h"

static const char *TAG = "net";

#define NVS_NS              "aemeath_wifi"
#define STA_FAILS_TO_PORTAL 5
#define PORTAL_RETRY_US     (30 * 1000 * 1000)

static SemaphoreHandle_t s_lock;
static net_status_t s_status;
static int s_fail_count;
static bool s_has_creds;
static bool s_mdns_started;
static esp_timer_handle_t s_retry_timer;
static wifi_config_t s_ap_cfg;

static void set_state(net_state_t st)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_status.state = st;
    xSemaphoreGive(s_lock);
}

void net_get_status(net_status_t *out)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    *out = s_status;
    xSemaphoreGive(s_lock);
    wifi_ap_record_t ap;
    if (out->state == NET_CONNECTED && esp_wifi_sta_get_ap_info(&ap) == ESP_OK) {
        out->rssi = ap.rssi;
    }
}

static void start_mdns(void)
{
    if (s_mdns_started) {
        return;
    }
    ESP_ERROR_CHECK(mdns_init());
    mdns_hostname_set(NET_MDNS_HOST);
    mdns_instance_name_set("Aemeath Monitor");
    mdns_txt_item_t txt[] = {{"path", "/ws"}, {"v", "1"}};
    mdns_service_add("Aemeath Monitor", NET_MDNS_SERVICE, "_tcp", 80, txt, 2);
    mdns_service_add(NULL, "_http", "_tcp", 80, NULL, 0);
    s_mdns_started = true;
}

static void start_portal(void)
{
    if (s_status.state == NET_PORTAL) {
        return;
    }
    ESP_LOGW(TAG, "starting config portal on SSID %s", s_status.ap_ssid);
    esp_wifi_set_mode(s_has_creds ? WIFI_MODE_APSTA : WIFI_MODE_AP);
    esp_wifi_set_config(WIFI_IF_AP, &s_ap_cfg);
    set_state(NET_PORTAL);
    dns_hijack_start();
    if (s_has_creds) {
        esp_timer_start_periodic(s_retry_timer, PORTAL_RETRY_US);
    }
}

static void stop_portal(void)
{
    esp_timer_stop(s_retry_timer);
    dns_hijack_stop();
    esp_wifi_set_mode(WIFI_MODE_STA);
}

static void retry_cb(void *arg)
{
    esp_wifi_connect();
}

static void on_wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    switch (id) {
    case WIFI_EVENT_STA_START:
        if (s_has_creds) {
            esp_wifi_connect();
        }
        break;
    case WIFI_EVENT_STA_DISCONNECTED: {
        wifi_event_sta_disconnected_t *ev = data;
        ESP_LOGW(TAG, "STA disconnected, reason %d", ev->reason);
        if (s_status.state == NET_PORTAL) {
            break;  // retry_cb handles it at a slower pace so AP clients aren't disrupted
        }
        set_state(NET_CONNECTING);
        if (++s_fail_count >= STA_FAILS_TO_PORTAL) {
            start_portal();
        } else {
            esp_wifi_connect();
        }
        break;
    }
    default:
        break;
    }
}

static void on_ip_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (id != IP_EVENT_STA_GOT_IP) {
        return;
    }
    ip_event_got_ip_t *ev = data;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    snprintf(s_status.ip, sizeof(s_status.ip), IPSTR, IP2STR(&ev->ip_info.ip));
    xSemaphoreGive(s_lock);
    ESP_LOGI(TAG, "got ip %s", s_status.ip);
    if (s_status.state == NET_PORTAL) {
        stop_portal();
    }
    s_fail_count = 0;
    set_state(NET_CONNECTED);
    start_mdns();
}

static bool load_credentials(wifi_config_t *cfg)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) {
        return false;
    }
    size_t ssid_len = sizeof(cfg->sta.ssid);
    size_t pass_len = sizeof(cfg->sta.password);
    bool ok = nvs_get_str(h, "ssid", (char *)cfg->sta.ssid, &ssid_len) == ESP_OK && ssid_len > 1;
    if (ok && nvs_get_str(h, "pass", (char *)cfg->sta.password, &pass_len) != ESP_OK) {
        cfg->sta.password[0] = 0;
    }
    nvs_close(h);
    return ok;
}

static void reboot_cb(void *arg)
{
    esp_restart();
}

static void reboot_soon(void)
{
    const esp_timer_create_args_t args = {.callback = reboot_cb, .name = "reboot"};
    esp_timer_handle_t t;
    esp_timer_create(&args, &t);
    esp_timer_start_once(t, 1500 * 1000);
}

void net_save_credentials(const char *ssid, const char *pass)
{
    nvs_handle_t h;
    ESP_ERROR_CHECK(nvs_open(NVS_NS, NVS_READWRITE, &h));
    nvs_set_str(h, "ssid", ssid);
    nvs_set_str(h, "pass", pass);
    nvs_commit(h);
    nvs_close(h);
    ESP_LOGI(TAG, "saved credentials for '%s', rebooting", ssid);
    reboot_soon();
}

void net_forget_credentials(void)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) == ESP_OK) {
        nvs_erase_all(h);
        nvs_commit(h);
        nvs_close(h);
    }
    ESP_LOGW(TAG, "credentials erased, rebooting");
    reboot_soon();
}

void net_start(void)
{
    s_lock = xSemaphoreCreateMutex();
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();
    esp_netif_create_default_wifi_ap();

    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init));
    ESP_ERROR_CHECK(esp_wifi_set_storage(WIFI_STORAGE_RAM));
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID, on_wifi_event, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_ip_event, NULL));

    const esp_timer_create_args_t targs = {.callback = retry_cb, .name = "sta_retry"};
    ESP_ERROR_CHECK(esp_timer_create(&targs, &s_retry_timer));

    uint8_t mac[6];
    esp_read_mac(mac, ESP_MAC_WIFI_SOFTAP);
    snprintf(s_status.ap_ssid, sizeof(s_status.ap_ssid), "Aemeath-%02X%02X", mac[4], mac[5]);

    s_ap_cfg.ap.channel = 1;
    s_ap_cfg.ap.max_connection = 4;
    s_ap_cfg.ap.authmode = WIFI_AUTH_OPEN;
    strlcpy((char *)s_ap_cfg.ap.ssid, s_status.ap_ssid, sizeof(s_ap_cfg.ap.ssid));
    s_ap_cfg.ap.ssid_len = strlen(s_status.ap_ssid);

    wifi_config_t sta = {0};
    s_has_creds = load_credentials(&sta);
    if (s_has_creds) {
        strlcpy(s_status.sta_ssid, (const char *)sta.sta.ssid, sizeof(s_status.sta_ssid));
        sta.sta.threshold.authmode = WIFI_AUTH_OPEN;
        ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
        ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &sta));
        s_status.state = NET_CONNECTING;
        ESP_LOGI(TAG, "connecting to '%s'", s_status.sta_ssid);
    } else {
        ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_AP));
        ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_AP, &s_ap_cfg));
    }
    ESP_ERROR_CHECK(esp_wifi_start());
    esp_wifi_set_ps(WIFI_PS_NONE);  // keep latency low; device is USB powered

    server_start();
    if (!s_has_creds) {
        start_portal();
    }
}
