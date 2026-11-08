// Subscription quotas forwarded by the agent from the Magpie gateway.
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "cJSON.h"

#define QUOTA_MAX_ACCOUNTS 8
#define QUOTA_MAX_WINDOWS  8

typedef struct {
    char name[32];
    char display[16];
    float used;     // % consumed
    int64_t reset;  // unix seconds, 0 if unknown
} quota_window_t;

typedef struct {
    char name[32];
    char plan[32];
    bool is_balance;
    char balance[64];  // formatted amount from Magpie, including currency
    int n;
    quota_window_t windows[QUOTA_MAX_WINDOWS];
} quota_account_t;

typedef struct {
    uint32_t seq;   // bumps on every accepted frame
    int64_t rx_us;
    int n;
    quota_account_t accounts[QUOTA_MAX_ACCOUNTS];
    bool has_usage;
    double cost_today, cost_30d;
    int64_t calls_today, calls_30d;
} quota_t;

void quota_init(void);
void quota_ingest(const cJSON *root);
void quota_snapshot(quota_t *out);
