// Background lifetime: one worker owns flash/codec/pixels; cheap status is copied
// under a spinlock. Never hold a status/LVGL lock during networking or flash IO.
#include "background.h"
#include "background_codec.h"
#include "background_format.h"
#include "ui/dashboard.h"
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_partition.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "psa/crypto.h"

static const char *TAG = "background";
static const esp_partition_t *s_partition;
static background_store_t s_store;
static background_status_t s_status;
static portMUX_TYPE s_status_lock = portMUX_INITIALIZER_UNLOCKED;
static int64_t s_deadline;
static uint8_t *s_pixels;
static bool s_ready; // fail closed if either slot could not be scanned

static bool proceeding(void *unused)
{
    (void)unused;
    return !s_deadline || esp_timer_get_time() < s_deadline;
}
void background_set_deadline(int64_t deadline_us) { s_deadline = deadline_us; }
bool background_sha256(const uint8_t *data, size_t len, uint8_t hash[32])
{
    size_t actual = 0;
    return psa_hash_compute(PSA_ALG_SHA_256, data, len, hash, 32, &actual) == PSA_SUCCESS && actual == 32;
}
static bool flash_read(void *unused, size_t offset, void *out, size_t len)
{
    (void)unused;
    return proceeding(NULL) && esp_partition_read(s_partition, offset, out, len) == ESP_OK;
}
static bool flash_write(void *unused, size_t offset, const void *data, size_t len)
{
    (void)unused;
    if (!proceeding(NULL)) return false;
    bool ok = esp_partition_write(s_partition, offset, data, len) == ESP_OK;
    vTaskDelay(1); // IO is sector bounded; preserve higher-priority audio work.
    return ok;
}
static bool flash_erase(void *unused, size_t offset, size_t len)
{
    (void)unused;
    if (!proceeding(NULL)) return false;
    bool ok = esp_partition_erase_range(s_partition, offset, len) == ESP_OK;
    vTaskDelay(1);
    return ok;
}
static bool flash_hash(void *unused, size_t offset, size_t len, uint8_t hash[32])
{
    (void)unused;
    uint8_t block[1024];
    psa_hash_operation_t op = PSA_HASH_OPERATION_INIT;
    bool ok = psa_hash_setup(&op, PSA_ALG_SHA_256) == PSA_SUCCESS;
    while (ok && len) {
        size_t n = len < sizeof(block) ? len : sizeof(block);
        ok = flash_read(NULL, offset, block, n) && psa_hash_update(&op, block, n) == PSA_SUCCESS;
        offset += n; len -= n;
    }
    size_t actual = 0;
    if (ok) ok = psa_hash_finish(&op, hash, 32, &actual) == PSA_SUCCESS && actual == 32;
    psa_hash_abort(&op);
    return ok;
}
static void publish(bool displayed, const char *error)
{
    background_status_t next = {.supported = s_partition != NULL,
        .present = s_store.record.present, .displayed = displayed,
        .revision = s_store.record.revision, .bytes = s_store.record.bytes};
    if (next.present) {
        next.width = BACKGROUND_WIDTH; next.height = BACKGROUND_HEIGHT;
        background_hex_sha256(s_store.record.sha256, next.sha256);
    }
    if (error) snprintf(next.error, sizeof(next.error), "%s", error);
    portENTER_CRITICAL(&s_status_lock);
    s_status = next;
    portEXIT_CRITICAL(&s_status_lock);
}
void background_snapshot(background_status_t *out)
{
    portENTER_CRITICAL(&s_status_lock);
    *out = s_status;
    portEXIT_CRITICAL(&s_status_lock);
}
static background_result_t store_result(background_store_result_t result)
{
    switch (result) {
    case BG_STORE_OK: return BG_OK;
    case BG_STORE_INVALID: case BG_STORE_EXHAUSTED: return BG_INVALID;
    case BG_STORE_CANCELLED: return BG_TIMEOUT;
    default: return proceeding(NULL) ? BG_IO : BG_TIMEOUT;
    }
}
static background_result_t apply(uint8_t *pixels)
{
    // A valid candidate is decoded before persistence. No allocation is needed
    // here: dashboard owns two static descriptors and an already-created object.
    // Bound lock acquisition to the remaining request deadline (boot gets 1s).
    int64_t left = s_deadline ? s_deadline - esp_timer_get_time() : 1000000;
    uint32_t timeout = left > 0 ? (uint32_t)((left + 999) / 1000) : 1;
    if (!dashboard_set_background(pixels, timeout)) {
        free(pixels);
        // This exceptional post-commit failure is explicitly discoverable: the
        // new digest/revision is durable, displayed=false; never lie about undo.
        publish(false, "background committed but display unavailable");
        return BG_IO;
    }
    uint8_t *old = s_pixels;
    s_pixels = pixels;
    free(old); // all old draw work and only old-source caches have been retired
    publish(pixels != NULL, NULL);
    return BG_OK;
}
background_result_t background_read_image(uint8_t **jpeg, size_t *length)
{
    *jpeg = NULL; *length = 0;
    if (!s_partition) return BG_UNSUPPORTED;
    if (!s_ready) return BG_IO;
    if (!s_store.record.present) return BG_NOT_FOUND;
    uint8_t *data = heap_caps_malloc(s_store.record.bytes, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (!data) return BG_NO_MEMORY;
    for (size_t pos = 0; pos < s_store.record.bytes;) {
        size_t n = s_store.record.bytes - pos;
        if (n > BACKGROUND_SECTOR_BYTES) n = BACKGROUND_SECTOR_BYTES;
        background_result_t err = store_result(background_store_read(&s_store, pos, data + pos, n));
        if (err != BG_OK) { free(data); return err; }
        pos += n;
    }
    *jpeg = data; *length = s_store.record.bytes;
    return BG_OK;
}
background_result_t background_put(uint64_t revision, uint8_t *data, size_t len, const uint8_t sha[32])
{
    if (!s_partition) return BG_UNSUPPORTED;
    if (!s_ready) return BG_IO;
    if (revision != s_store.record.revision) return BG_CONFLICT;
    if (!proceeding(NULL)) return BG_TIMEOUT;
    if (!background_jpeg_valid(data, len)) return BG_INVALID;
    uint8_t actual[32];
    if (!background_sha256(data, len, actual)) return BG_NO_MEMORY;
    if (memcmp(actual, sha, 32)) return BG_INVALID;
    background_status_t status;
    background_snapshot(&status);
    if (status.displayed && len == s_store.record.bytes && !memcmp(sha, s_store.record.sha256, 32)) return BG_OK;
    uint8_t *pixels;
    background_codec_result_t decoded = background_decode_jpeg(data, len, &pixels);
    if (decoded != BG_CODEC_OK) return decoded == BG_CODEC_NO_MEMORY ? BG_NO_MEMORY : BG_INVALID;
    background_result_t err = store_result(background_store_commit(&s_store, data, len, sha));
    if (err != BG_OK) { free(pixels); return err; }
    return apply(pixels);
}
background_result_t background_delete(uint64_t revision)
{
    if (!s_partition) return BG_UNSUPPORTED;
    if (!s_ready) return BG_IO;
    if (revision != s_store.record.revision) return BG_CONFLICT;
    background_result_t err = store_result(background_store_commit(&s_store, NULL, 0, NULL));
    if (err != BG_OK) return err;
    return apply(NULL);
}
void background_init(void)
{
    s_ready = false;
    s_partition = esp_partition_find_first(ESP_PARTITION_TYPE_DATA, ESP_PARTITION_SUBTYPE_ANY, "background");
    if (!s_partition || s_partition->address != BACKGROUND_PARTITION_OFFSET || s_partition->size != BACKGROUND_PARTITION_BYTES) {
        s_partition = NULL;
        publish(false, "background partition unavailable; upgrade partition table");
        ESP_LOGW(TAG, "background partition unavailable; dashboard remains plain");
        return;
    }
    if (psa_crypto_init() != PSA_SUCCESS) {
        s_partition = NULL;
        publish(false, "background hash initialization failed");
        return;
    }
    background_flash_t flash = {.read = flash_read, .write = flash_write, .erase = flash_erase,
                               .hash = flash_hash, .proceed = proceeding};
    background_store_result_t opened = background_store_open(&s_store, flash);
    if (opened != BG_STORE_OK) {
        // The unread slot may be newer than the apparently valid slot. Never
        // apply that stale record, erase the unknown slot, or clear this error
        // through apply(). Retry only after a fresh full scan on reboot.
        publish(false, "background flash scan failed; restart device");
        return;
    }
    s_ready = true;
    publish(false, NULL);
    if (!s_store.record.present) return;
    uint8_t *jpeg, *pixels;
    size_t len;
    background_result_t err = background_read_image(&jpeg, &len);
    if (err != BG_OK) { publish(false, "saved background read/allocation failed"); return; }
    background_codec_result_t decoded = background_decode_jpeg(jpeg, len, &pixels);
    free(jpeg);
    if (decoded != BG_CODEC_OK) { publish(false, "saved background decode/allocation failed"); return; }
    if (apply(pixels) == BG_OK) ESP_LOGI(TAG, "restored background revision %llu", (unsigned long long)s_store.record.revision);
}
