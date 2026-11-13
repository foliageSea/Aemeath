// Exercises the real production A/B implementation with deterministic flash.
#include "background_store.h"
#include "background_format.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    uint8_t bytes[BACKGROUND_PARTITION_BYTES];
    unsigned calls, fail_at, cut, gates, cancel_at;
} fake_t;
static fake_t flash, baseline;
static bool step(fake_t *f) { return ++f->calls != f->fail_at; }
static bool bounds(size_t at, size_t n) { return at <= BACKGROUND_PARTITION_BYTES && n <= BACKGROUND_PARTITION_BYTES - at; }
static bool rd(void *ctx, size_t at, void *p, size_t n)
{
    fake_t *f = ctx; assert(bounds(at, n));
    if (!step(f)) return false;
    memcpy(p, f->bytes + at, n); return true;
}
static bool wr(void *ctx, size_t at, const void *p, size_t n)
{
    fake_t *f = ctx; assert(bounds(at, n));
    bool ok = step(f);
    size_t write_n = ok ? n : (f->cut < n ? f->cut : n);
    const uint8_t *source = p;
    for (size_t i = 0; i < write_n; ++i) { assert((f->bytes[at + i] & source[i]) == source[i]); f->bytes[at + i] &= source[i]; }
    return ok;
}
static bool er(void *ctx, size_t at, size_t n)
{
    fake_t *f = ctx; assert(bounds(at, n) && at % 4096 == 0 && n == 4096);
    bool ok = step(f);
    memset(f->bytes + at, 0xff, ok ? n : (f->cut < n ? f->cut : n)); return ok;
}
// Deliberately not a second SHA implementation: this adapter verifies storage
// compares 32-byte digests; runtime adapter uses PSA SHA-256 on the real target.
static void digest(const uint8_t *p, size_t n, uint8_t out[32])
{
    uint32_t h = 2166136261u;
    for (size_t i = 0; i < n; ++i) h = (h ^ p[i]) * 16777619u;
    for (unsigned i = 0; i < 32; ++i) { h = h * 1664525u + 1013904223u; out[i] = (uint8_t)(h >> 24); }
}
static bool hash(void *ctx, size_t at, size_t n, uint8_t out[32])
{
    fake_t *f = ctx; assert(bounds(at, n));
    if (!step(f)) return false;
    digest(f->bytes + at, n, out); return true;
}
static bool gate(void *ctx) { fake_t *f = ctx; return ++f->gates != f->cancel_at; }
static background_flash_t io(fake_t *f) { return (background_flash_t){f, rd, wr, er, hash, gate}; }
static void reset_counts(fake_t *f) { f->calls = f->fail_at = f->cut = f->gates = f->cancel_at = 0; }
static background_store_t reboot(void)
{
    reset_counts(&flash);
    background_store_t s;
    assert(background_store_open(&s, io(&flash)) == BG_STORE_OK);
    reset_counts(&flash);
    return s;
}
static uint8_t image_a[8501], image_b[9103], max_image[BACKGROUND_MAX_BYTES];
static uint8_t sha_a[32], sha_b[32], sha_max[32];
static void seed(void)
{
    memset(&flash, 0, sizeof(flash)); memset(flash.bytes, 0xff, sizeof(flash.bytes));
    background_store_t s = reboot();
    assert(!s.record.present && !s.record.revision && s.record.slot == -1);
    assert(background_store_commit(&s, image_a, sizeof(image_a), sha_a) == BG_STORE_OK);
    assert(s.record.present && s.record.revision == 1 && s.record.slot == 0);
    baseline = flash; reset_counts(&baseline);
}
static void assert_record(background_store_t *s, uint64_t revision, bool present)
{
    assert(s->record.revision == revision && s->record.present == present);
    if (present) {
        uint8_t copy[sizeof(image_b)];
        size_t n = revision == 1 ? sizeof(image_a) : sizeof(image_b);
        assert(background_store_read(s, 0, copy, n) == BG_STORE_OK);
        assert(!memcmp(copy, revision == 1 ? image_a : image_b, n));
    }
}
static void power_failures(void)
{
    seed();
    background_store_t s = reboot();
    assert(background_store_commit(&s, image_b, sizeof(image_b), sha_b) == BG_STORE_OK);
    unsigned operations = flash.calls, gates = flash.gates;
    for (unsigned fail = 1; fail <= operations; ++fail) for (unsigned cut = 0; cut <= 4; ++cut) {
        flash = baseline; s = reboot(); flash.fail_at = fail; flash.cut = cut;
        background_store_result_t result = background_store_commit(&s, image_b, sizeof(image_b), sha_b);
        assert(result != BG_STORE_OK);
        assert(s.record.revision == 1); // no in-memory activation before commit success
        assert(!memcmp(flash.bytes, baseline.bytes, BACKGROUND_SLOT_BYTES));
        s = reboot();
        // A driver can report error after programming all 4 commit bytes. This
        // is indistinguishable from a lost success response, and must recover new.
        assert(s.record.revision == 1 || s.record.revision == 2);
        assert_record(&s, s.record.revision, true);
    }
    for (unsigned cancel = 1; cancel <= gates; ++cancel) {
        flash = baseline; s = reboot(); flash.cancel_at = cancel;
        assert(background_store_commit(&s, image_b, sizeof(image_b), sha_b) == BG_STORE_CANCELLED);
        s = reboot(); assert_record(&s, 1, true);
    }
    printf("PASS: %u erase/write/read/hash failure points x5 partial writes, %u cancellation points\n", operations, gates);
}
static void delete_and_corruption(void)
{
    seed(); background_store_t s = reboot();
    assert(background_store_commit(&s, image_b, sizeof(image_b), sha_b) == BG_STORE_OK);
    baseline = flash; reset_counts(&baseline);
    reset_counts(&flash);
    assert(background_store_commit(&s, NULL, 0, NULL) == BG_STORE_OK);
    unsigned operations = flash.calls;
    for (unsigned fail = 1; fail <= operations; ++fail) for (unsigned cut = 0; cut <= 4; ++cut) {
        flash = baseline; s = reboot(); flash.fail_at = fail; flash.cut = cut;
        assert(background_store_commit(&s, NULL, 0, NULL) != BG_STORE_OK);
        s = reboot(); assert(s.record.revision == 2 || s.record.revision == 3);
        assert(s.record.present == (s.record.revision == 2));
    }
    flash = baseline; s = reboot();
    assert(background_store_commit(&s, NULL, 0, NULL) == BG_STORE_OK);
    s = reboot(); assert(!s.record.present && s.record.revision == 3);
    assert(background_store_commit(&s, NULL, 0, NULL) == BG_STORE_OK && flash.calls == 0);
    assert(background_store_commit(&s, image_b, sizeof(image_b), sha_b) == BG_STORE_OK);
    s = reboot(); assert_record(&s, 4, true);
    assert(background_store_commit(&s, NULL, 0, NULL) == BG_STORE_OK);
    s = reboot(); assert(!s.record.present && s.record.revision == 5);

    seed(); s = reboot();
    assert(background_store_commit(&s, image_b, sizeof(image_b), sha_b) == BG_STORE_OK);
    flash.bytes[BACKGROUND_SLOT_BYTES + BACKGROUND_SECTOR_BYTES + 10] ^= 1;
    s = reboot(); assert_record(&s, 1, true); // bad latest payload cannot activate
    flash.bytes[0] ^= 1;
    s = reboot(); assert(!s.record.present && !s.record.revision);
    puts("PASS: committed EMPTY survives reboots, no resurrection, corrupted-slot recovery");
}
static void bounds_and_noops(void)
{
    seed(); background_store_t s = reboot();
    assert(background_store_commit(&s, image_a, sizeof(image_a), sha_a) == BG_STORE_OK && !flash.calls);
    assert(background_store_commit(&s, max_image, sizeof(max_image), sha_max) == BG_STORE_OK);
    uint8_t *copy = malloc(BACKGROUND_MAX_BYTES); assert(copy);
    assert(background_store_read(&s, 0, copy, BACKGROUND_MAX_BYTES) == BG_STORE_OK && !memcmp(copy, max_image, BACKGROUND_MAX_BYTES));
    assert(background_store_read(&s, BACKGROUND_MAX_BYTES, copy, 1) == BG_STORE_INVALID);
    assert(background_store_read(&s, SIZE_MAX, copy, 1) == BG_STORE_INVALID);
    assert(background_store_commit(&s, copy, BACKGROUND_MAX_BYTES + 1, sha_max) == BG_STORE_INVALID);
    assert(background_store_commit(&s, NULL, 1, sha_a) == BG_STORE_INVALID);
    s.record.revision = UINT64_MAX;
    assert(background_store_commit(&s, image_b, sizeof(image_b), sha_b) == BG_STORE_EXHAUSTED);
    free(copy);
    puts("PASS: max payload, bounds/overflow, duplicate writes and revision exhaustion");
}
static void format_checks(void)
{
    uint64_t revision;
    assert(background_parse_etag("\"0\"", &revision) && revision == 0);
    assert(background_parse_etag("\"18446744073709551615\"", &revision) && revision == UINT64_MAX);
    const char *bad[] = {"", "0", "\"\"", "\"01\"", "*", "W/\"1\"", "\"1\",\"2\"", "\"18446744073709551616\"", "\"-1\"", "\"1\" "};
    for (unsigned i = 0; i < sizeof(bad) / sizeof(bad[0]); ++i) assert(!background_parse_etag(bad[i], &revision));
    char text[65]; uint8_t decoded[32];
    background_hex_sha256(sha_a, text);
    assert(background_parse_sha256(text, decoded) && !memcmp(decoded, sha_a, 32));
    text[63] = 'G'; assert(!background_parse_sha256(text, decoded));
    assert(!background_parse_sha256("a", decoded));
    // Minimal structurally valid baseline fixture (entropy is decoded only by codec).
    uint8_t jpeg[] = {0xff,0xd8,0xff,0xc0,0,11,8,1,0xe0,1,0xe0,1,1,0x11,0,
                      0xff,0xda,0,8,1,1,0,0,63,0,0x18,0xff,0,0x30,0xff,0xd0,0xff,0xd9};
    assert(background_jpeg_valid(jpeg, sizeof(jpeg)));
    for (size_t n = 0; n < sizeof(jpeg); ++n) assert(!background_jpeg_valid(jpeg, n));
    jpeg[3] = 0xc2; assert(!background_jpeg_valid(jpeg, sizeof(jpeg))); jpeg[3] = 0xc0;
    jpeg[8] = 0xdf; assert(!background_jpeg_valid(jpeg, sizeof(jpeg))); jpeg[8] = 0xe0;
    jpeg[4] = 0xff; assert(!background_jpeg_valid(jpeg, sizeof(jpeg))); jpeg[4] = 0;
    jpeg[23] = 62; assert(!background_jpeg_valid(jpeg, sizeof(jpeg)));
    uint8_t fuzz[512]; unsigned x = 1;
    for (unsigned k = 0; k < 10000; ++k) {
        for (unsigned i = 0; i < sizeof(fuzz); ++i) { x = x * 1664525 + 1013904223; fuzz[i] = (uint8_t)(x >> 24); }
        (void)background_jpeg_valid(fuzz, k % sizeof(fuzz));
    }
    puts("PASS: strict uint64 ETag/SHA, baseline geometry/truncation guards and bounded parser fuzz");
}
int main(void)
{
    for (size_t i = 0; i < sizeof(image_a); ++i) image_a[i] = (uint8_t)(i * 13);
    for (size_t i = 0; i < sizeof(image_b); ++i) image_b[i] = (uint8_t)(i * 17 + 4);
    for (size_t i = 0; i < sizeof(max_image); ++i) max_image[i] = (uint8_t)(i * 19 + 7);
    digest(image_a, sizeof(image_a), sha_a); digest(image_b, sizeof(image_b), sha_b); digest(max_image, sizeof(max_image), sha_max);
    power_failures(); delete_and_corruption(); bounds_and_noops(); format_checks();
    return 0;
}
