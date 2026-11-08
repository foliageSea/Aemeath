#include "metrics.h"

#include <string.h>
#include <sys/time.h>

#include "cJSON.h"
#include "dashboard.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "quota.h"

static metrics_t s_m;
static SemaphoreHandle_t s_lock;

static void reset_numbers(metrics_t *m)
{
    m->uptime = m->cpu = m->cpu_temp = m->cpu_freq = NAN;
    m->mem_used = m->mem_total = m->swap_used = m->swap_total = NAN;
    m->disk_used = m->disk_total = m->disk_rd = m->disk_wr = NAN;
    m->net_up = m->net_down = NAN;
    m->gpu_load = m->gpu_temp = m->gpu_mem_used = m->gpu_mem_total = NAN;
    m->ncores = 0;
    m->cpu_name[0] = m->gpu_name[0] = m->ip[0] = 0;
}

void metrics_init(void)
{
    s_lock = xSemaphoreCreateMutex();
    reset_numbers(&s_m);
}

static double num(const cJSON *obj, const char *key)
{
    const cJSON *it = cJSON_GetObjectItemCaseSensitive(obj, key);
    return cJSON_IsNumber(it) ? it->valuedouble : NAN;
}

static void str(const cJSON *obj, const char *key, char *dst, size_t cap)
{
    const cJSON *it = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (cJSON_IsString(it)) {
        strlcpy(dst, it->valuestring, cap);
    }
}

bool metrics_ingest(const char *json, size_t len)
{
    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!root) {
        return false;
    }
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(root, "type");
    if (cJSON_IsString(type)) {
        bool accepted = true;
        if (strcmp(type->valuestring, "quota") == 0) {
            quota_ingest(root);
        } else if (strcmp(type->valuestring, "display") == 0) {
            const cJSON *v = cJSON_GetObjectItemCaseSensitive(root, "v");
            const cJSON *page = cJSON_GetObjectItemCaseSensitive(root, "page");
            accepted = false;
            if (cJSON_IsNumber(v) && v->valuedouble == 1 && cJSON_IsString(page)) {
                if (strcmp(page->valuestring, "metrics") == 0) {
                    accepted = dashboard_show_page(DASHBOARD_PAGE_METRICS);
                } else if (strcmp(page->valuestring, "quota") == 0) {
                    accepted = dashboard_show_page(DASHBOARD_PAGE_QUOTA);
                }
            }
        }
        cJSON_Delete(root);
        return accepted;
    }
    metrics_t m;
    memset(&m, 0, sizeof(m));
    reset_numbers(&m);

    str(root, "host", m.host, sizeof(m.host));
    str(root, "os", m.os, sizeof(m.os));
    double ts = num(root, "ts");
    double tz = num(root, "tz");
    m.ts = isnan(ts) ? 0 : (int64_t)ts;
    m.tz = isnan(tz) ? 0 : (int32_t)tz;
    m.uptime = num(root, "uptime");

    const cJSON *cpu = cJSON_GetObjectItemCaseSensitive(root, "cpu");
    if (cJSON_IsObject(cpu)) {
        m.cpu = num(cpu, "total");
        m.cpu_temp = num(cpu, "temp");
        m.cpu_freq = num(cpu, "freq");
        str(cpu, "name", m.cpu_name, sizeof(m.cpu_name));
        const cJSON *cores = cJSON_GetObjectItemCaseSensitive(cpu, "cores");
        const cJSON *c;
        cJSON_ArrayForEach(c, cores) {
            if (m.ncores >= METRICS_MAX_CORES) break;
            m.cores[m.ncores++] = cJSON_IsNumber(c) ? (float)c->valuedouble : 0;
        }
    }
    const cJSON *mem = cJSON_GetObjectItemCaseSensitive(root, "mem");
    if (cJSON_IsObject(mem)) {
        m.mem_used = num(mem, "used");
        m.mem_total = num(mem, "total");
        m.swap_used = num(mem, "swap_used");
        m.swap_total = num(mem, "swap_total");
    }
    const cJSON *disk = cJSON_GetObjectItemCaseSensitive(root, "disk");
    if (cJSON_IsObject(disk)) {
        m.disk_used = num(disk, "used");
        m.disk_total = num(disk, "total");
        m.disk_rd = num(disk, "rd");
        m.disk_wr = num(disk, "wr");
    }
    const cJSON *net = cJSON_GetObjectItemCaseSensitive(root, "net");
    if (cJSON_IsObject(net)) {
        m.net_up = num(net, "up");
        m.net_down = num(net, "down");
        str(net, "ip", m.ip, sizeof(m.ip));
    }
    const cJSON *gpu = cJSON_GetObjectItemCaseSensitive(root, "gpu");
    if (cJSON_IsObject(gpu)) {
        m.gpu_load = num(gpu, "load");
        m.gpu_temp = num(gpu, "temp");
        m.gpu_mem_used = num(gpu, "mem_used");
        m.gpu_mem_total = num(gpu, "mem_total");
        str(gpu, "name", m.gpu_name, sizeof(m.gpu_name));
    }
    cJSON_Delete(root);

    // The PC clock is the device's only time source.
    if (m.ts > 1600000000) {
        struct timeval tv = {.tv_sec = m.ts};
        settimeofday(&tv, NULL);
    }

    xSemaphoreTake(s_lock, portMAX_DELAY);
    m.seq = s_m.seq + 1;
    m.rx_us = esp_timer_get_time();
    s_m = m;
    xSemaphoreGive(s_lock);
    return true;
}

void metrics_snapshot(metrics_t *out)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    *out = s_m;
    xSemaphoreGive(s_lock);
}
