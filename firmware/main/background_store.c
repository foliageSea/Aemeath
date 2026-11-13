// Power-loss-safe A/B records. All on-flash integers are little endian.
#include "background_store.h"
#include <limits.h>
#include <string.h>

#define MAGIC 0x31474241u // ABG1
#define COMMITTED 0x31544d43u // CMT1; excluded from the header CRC

static uint32_t get32(const uint8_t *p)
{
    return (uint32_t)p[0] | (uint32_t)p[1] << 8 | (uint32_t)p[2] << 16 | (uint32_t)p[3] << 24;
}
static void put32(uint8_t *p, uint32_t n)
{
    for (unsigned i = 0; i < 4; ++i) p[i] = (uint8_t)(n >> (8 * i));
}
static uint64_t get64(const uint8_t *p) { return get32(p) | (uint64_t)get32(p + 4) << 32; }
static void put64(uint8_t *p, uint64_t n) { put32(p, (uint32_t)n); put32(p + 4, (uint32_t)(n >> 32)); }
static uint32_t crc32(const uint8_t *p, size_t n)
{
    uint32_t crc = UINT32_MAX;
    for (size_t i = 0; i < n; ++i) {
        crc ^= p[i];
        for (unsigned j = 0; j < 8; ++j) crc = (crc >> 1) ^ (0xedb88320u & (0u - (crc & 1u)));
    }
    return ~crc;
}
static bool proceed(background_store_t *s) { return !s->flash.proceed || s->flash.proceed(s->flash.ctx); }

static background_store_result_t inspect(background_store_t *s, int slot, background_record_t *out)
{
    uint8_t h[BACKGROUND_HEADER_BYTES];
    size_t base = (size_t)slot * BACKGROUND_SLOT_BYTES;
    if (!proceed(s)) return BG_STORE_CANCELLED;
    if (!s->flash.read(s->flash.ctx, base, h, sizeof(h))) return BG_STORE_IO;
    if (get32(h) != MAGIC || get32(h + 4) != 1 || get32(h + 68) != COMMITTED ||
        get32(h + 64) != crc32(h, 64) || !get64(h + 8)) return BG_STORE_INVALID;
    uint32_t kind = get32(h + 16), len = get32(h + 20);
    if (kind > 1 || len > BACKGROUND_MAX_BYTES ||
        (kind == 1 && (!len || get32(h + 24) != BACKGROUND_WIDTH || get32(h + 28) != BACKGROUND_HEIGHT)) ||
        (kind == 0 && (len || get32(h + 24) || get32(h + 28)))) return BG_STORE_INVALID;
    if (kind) {
        uint8_t hash[32];
        if (!s->flash.hash(s->flash.ctx, base + BACKGROUND_SECTOR_BYTES, len, hash)) return BG_STORE_IO;
        if (memcmp(hash, h + 32, 32)) return BG_STORE_INVALID;
    } else {
        static const uint8_t zero[32];
        if (memcmp(h + 32, zero, sizeof(zero))) return BG_STORE_INVALID;
    }
    *out = (background_record_t){.slot = slot, .revision = get64(h + 8), .bytes = len, .present = kind == 1};
    memcpy(out->sha256, h + 32, 32);
    return BG_STORE_OK;
}

background_store_result_t background_store_open(background_store_t *s, background_flash_t flash)
{
    *s = (background_store_t){.flash = flash, .record = {.slot = -1}};
    if (!flash.read || !flash.write || !flash.erase || !flash.hash) return BG_STORE_INVALID;
    background_store_result_t result = BG_STORE_OK;
    for (int slot = 0; slot < 2; ++slot) {
        background_record_t rec;
        background_store_result_t err = inspect(s, slot, &rec);
        if (err == BG_STORE_OK && rec.revision > s->record.revision) s->record = rec;
        if (err == BG_STORE_CANCELLED || err == BG_STORE_IO) result = err;
    }
    return result;
}

background_store_result_t background_store_read(background_store_t *s, size_t offset, void *out, size_t len)
{
    if (!s->record.present || offset > s->record.bytes || len > s->record.bytes - offset) return BG_STORE_INVALID;
    if (!proceed(s)) return BG_STORE_CANCELLED;
    size_t base = (size_t)s->record.slot * BACKGROUND_SLOT_BYTES + BACKGROUND_SECTOR_BYTES;
    return s->flash.read(s->flash.ctx, base + offset, out, len) ? BG_STORE_OK : BG_STORE_IO;
}

background_store_result_t background_store_commit(background_store_t *s, const uint8_t *data, size_t len, const uint8_t sha[32])
{
    if (len > BACKGROUND_MAX_BYTES || (len && (!data || !sha))) return BG_STORE_INVALID;
    if ((!len && !s->record.present) || (len == s->record.bytes && len && !memcmp(sha, s->record.sha256, 32))) return BG_STORE_OK;
    if (s->record.revision == UINT64_MAX) return BG_STORE_EXHAUSTED;
    int slot = s->record.slot == 0 ? 1 : 0;
    size_t base = (size_t)slot * BACKGROUND_SLOT_BYTES;
    // Do not erase the currently committed slot, even on repeated failures.
    size_t erase_bytes = BACKGROUND_SECTOR_BYTES + ((len + BACKGROUND_SECTOR_BYTES - 1) / BACKGROUND_SECTOR_BYTES) * BACKGROUND_SECTOR_BYTES;
    for (size_t pos = 0; pos < erase_bytes; pos += BACKGROUND_SECTOR_BYTES) {
        if (!proceed(s)) return BG_STORE_CANCELLED;
        if (!s->flash.erase(s->flash.ctx, base + pos, BACKGROUND_SECTOR_BYTES)) return BG_STORE_IO;
    }
    for (size_t pos = 0; pos < len;) {
        size_t n = len - pos < BACKGROUND_SECTOR_BYTES ? len - pos : BACKGROUND_SECTOR_BYTES;
        if (!proceed(s)) return BG_STORE_CANCELLED;
        if (!s->flash.write(s->flash.ctx, base + BACKGROUND_SECTOR_BYTES + pos, data + pos, n)) return BG_STORE_IO;
        pos += n;
    }
    if (len) {
        uint8_t hash[32];
        if (!proceed(s)) return BG_STORE_CANCELLED;
        if (!s->flash.hash(s->flash.ctx, base + BACKGROUND_SECTOR_BYTES, len, hash)) return BG_STORE_IO;
        if (memcmp(hash, sha, 32)) return BG_STORE_INVALID;
    }
    uint8_t h[BACKGROUND_HEADER_BYTES];
    memset(h, 0, sizeof(h));
    put32(h, MAGIC); put32(h + 4, 1); put64(h + 8, s->record.revision + 1);
    put32(h + 16, len ? 1 : 0); put32(h + 20, (uint32_t)len);
    put32(h + 24, len ? BACKGROUND_WIDTH : 0); put32(h + 28, len ? BACKGROUND_HEIGHT : 0);
    if (len) memcpy(h + 32, sha, 32);
    put32(h + 64, crc32(h, 64));
    if (!proceed(s)) return BG_STORE_CANCELLED;
    if (!s->flash.write(s->flash.ctx, base, h, BACKGROUND_COMMIT_OFFSET)) return BG_STORE_IO;
    uint8_t verify[BACKGROUND_COMMIT_OFFSET];
    if (!s->flash.read(s->flash.ctx, base, verify, sizeof(verify)) || memcmp(verify, h, sizeof(verify))) return BG_STORE_IO;
    if (!proceed(s)) return BG_STORE_CANCELLED;
    put32(h + 68, COMMITTED);
    // Commit is a single separately programmed word; never rewrite it or check a
    // cancellation after it and then pretend the persisted operation was undone.
    if (!s->flash.write(s->flash.ctx, base + 68, h + 68, 4)) return BG_STORE_IO;
    s->record = (background_record_t){.slot = slot, .revision = get64(h + 8), .bytes = (uint32_t)len, .present = len != 0};
    memcpy(s->record.sha256, h + 32, 32);
    return BG_STORE_OK;
}
