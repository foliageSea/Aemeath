#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define BACKGROUND_MAX_BYTES 196608u
#define BACKGROUND_WIDTH 480u
#define BACKGROUND_HEIGHT 480u
#define BACKGROUND_RGB_BYTES (BACKGROUND_WIDTH * BACKGROUND_HEIGHT * 2u)
#define BACKGROUND_PARTITION_OFFSET 0x610000u
#define BACKGROUND_PARTITION_BYTES 0x80000u
#define BACKGROUND_SLOT_BYTES 0x40000u
#define BACKGROUND_SECTOR_BYTES 4096u
#define BACKGROUND_HEADER_BYTES 72u
#define BACKGROUND_COMMIT_OFFSET 68u

// All callbacks return false on failure. Storage has no heap or ESP-IDF dependency.
// Only one owner may call these functions; readers use a separate state snapshot.
typedef struct {
    void *ctx;
    bool (*read)(void *, size_t, void *, size_t);
    bool (*write)(void *, size_t, const void *, size_t);
    bool (*erase)(void *, size_t, size_t);
    bool (*hash)(void *, size_t, size_t, uint8_t[32]);
    // Optional deadline/cancellation gate, checked before each operation and commit.
    bool (*proceed)(void *);
} background_flash_t;

typedef struct {
    int slot; // -1: never configured
    uint64_t revision;
    uint32_t bytes;
    uint8_t sha256[32];
    bool present;
} background_record_t;

typedef struct {
    background_flash_t flash;
    background_record_t record;
} background_store_t;

typedef enum {
    BG_STORE_OK, BG_STORE_IO, BG_STORE_INVALID, BG_STORE_CANCELLED, BG_STORE_EXHAUSTED
} background_store_result_t;

// Empty/erased flash is normal. Invalid records are ignored; valid EMPTY records
// participate in revision ordering and must never fall back to an older JPEG.
background_store_result_t background_store_open(background_store_t *, background_flash_t);
background_store_result_t background_store_read(background_store_t *, size_t, void *, size_t);
// Caller validates/decodes JPEG first; len=0 records a durable EMPTY tombstone.
// SHA-256 is checked by readback before the final commit word is programmed.
background_store_result_t background_store_commit(background_store_t *, const uint8_t *, size_t, const uint8_t[32]);
