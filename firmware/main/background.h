#pragma once
#include "background_store.h"

typedef struct {
    bool supported, present, displayed;
    uint64_t revision;
    uint32_t width, height, bytes;
    char sha256[65];
    char error[96];
} background_status_t;

typedef enum {
    BG_OK, BG_UNSUPPORTED, BG_INVALID, BG_NO_MEMORY, BG_IO, BG_TIMEOUT, BG_CONFLICT, BG_NOT_FOUND
} background_result_t;

// Optional subsystem: errors never abort device startup. Call after dashboard and
// voice allocations, before the HTTP worker starts. No built-in image fallback.
void background_init(void);
void background_snapshot(background_status_t *);
// The following APIs have one owner: the background HTTP worker. deadline=0
// disables its gate (boot only). Snapshot remains nonblocking on other tasks.
void background_set_deadline(int64_t deadline_us);
background_result_t background_put(uint64_t expected_revision, uint8_t *, size_t, const uint8_t sha256[32]);
background_result_t background_delete(uint64_t expected_revision);
background_result_t background_read_image(uint8_t **jpeg, size_t *length);
bool background_sha256(const uint8_t *, size_t, uint8_t[32]);
