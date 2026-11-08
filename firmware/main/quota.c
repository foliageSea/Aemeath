#include "quota.h"

#include <string.h>

#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

static const char *TAG = "quota";

static quota_t s_q;
static SemaphoreHandle_t s_lock;

void quota_init(void)
{
    s_lock = xSemaphoreCreateMutex();
}

static void copy_str(const cJSON *obj, const char *key, char *dst, size_t cap)
{
    const cJSON *it = cJSON_GetObjectItemCaseSensitive(obj, key);
    dst[0] = 0;
    if (cJSON_IsString(it)) {
        strlcpy(dst, it->valuestring, cap);
    }
}

static double get_num(const cJSON *obj, const char *key)
{
    const cJSON *it = cJSON_GetObjectItemCaseSensitive(obj, key);
    return cJSON_IsNumber(it) ? it->valuedouble : 0;
}

void quota_ingest(const cJSON *root)
{
    // Built off-lock; quota_t is ~3 KB so keep it off the httpd stack.
    static quota_t q;
    memset(&q, 0, sizeof(q));

    const cJSON *acc;
    cJSON_ArrayForEach(acc, cJSON_GetObjectItemCaseSensitive(root, "accounts")) {
        if (q.n == QUOTA_MAX_ACCOUNTS) break;
        quota_account_t *a = &q.accounts[q.n++];
        copy_str(acc, "name", a->name, sizeof(a->name));
        copy_str(acc, "plan", a->plan, sizeof(a->plan));
        char kind[16];
        copy_str(acc, "kind", kind, sizeof(kind));
        a->is_balance = strcmp(kind, "balance") == 0;
        copy_str(acc, "balance", a->balance, sizeof(a->balance));
        const cJSON *w;
        cJSON_ArrayForEach(w, cJSON_GetObjectItemCaseSensitive(acc, "windows")) {
            if (a->n == QUOTA_MAX_WINDOWS) break;
            quota_window_t *win = &a->windows[a->n++];
            copy_str(w, "name", win->name, sizeof(win->name));
            copy_str(w, "display", win->display, sizeof(win->display));
            win->used = get_num(w, "used");
            win->reset = (int64_t)get_num(w, "reset");
        }
    }
    const cJSON *u = cJSON_GetObjectItemCaseSensitive(root, "usage");
    if (cJSON_IsObject(u)) {
        q.has_usage = true;
        q.cost_today = get_num(u, "cost_today");
        q.cost_30d = get_num(u, "cost_30d");
        q.calls_today = (int64_t)get_num(u, "calls_today");
        q.calls_30d = (int64_t)get_num(u, "calls_30d");
    }

    xSemaphoreTake(s_lock, portMAX_DELAY);
    q.seq = s_q.seq + 1;
    q.rx_us = esp_timer_get_time();
    s_q = q;
    xSemaphoreGive(s_lock);
    ESP_LOGI(TAG, "quota: %d accounts", q.n);
}

void quota_snapshot(quota_t *out)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    *out = s_q;
    xSemaphoreGive(s_lock);
}
