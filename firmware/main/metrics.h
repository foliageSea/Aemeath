// Latest metrics frame pushed by the PC agent.
#pragma once

#include <math.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define METRICS_MAX_CORES 32

// Unavailable numeric fields are NAN.
typedef struct {
    uint32_t seq;          // bumps on every accepted frame
    int64_t rx_us;         // esp_timer time of last frame
    char host[48];
    char os[16];
    int64_t ts;            // PC unix time
    int32_t tz;            // PC UTC offset, seconds
    double uptime;

    char cpu_name[64];
    double cpu, cpu_temp, cpu_freq;
    int ncores;
    float cores[METRICS_MAX_CORES];

    double mem_used, mem_total, swap_used, swap_total;
    double disk_used, disk_total, disk_rd, disk_wr;
    double net_up, net_down;
    char ip[40];

    char gpu_name[64];
    double gpu_load, gpu_temp, gpu_mem_used, gpu_mem_total;
} metrics_t;

void metrics_init(void);
// Parses one JSON frame; returns false on malformed input.
bool metrics_ingest(const char *json, size_t len);
// Copies the latest frame out under the lock.
void metrics_snapshot(metrics_t *out);
