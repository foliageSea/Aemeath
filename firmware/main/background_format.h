#pragma once
#include "background_store.h"

// Strict single strong ETag: quoted canonical unsigned decimal (no wildcard,
// weak validator, lists, leading zeros or silent overflow).
bool background_parse_etag(const char *, uint64_t *);
bool background_parse_sha256(const char *, uint8_t[32]);
void background_hex_sha256(const uint8_t[32], char[65]);
// Bounded structural guard before handing untrusted input to the chip decoder.
// Accepts only 8-bit baseline, one complete scan, 480x480, gray or 3 channels.
bool background_jpeg_valid(const uint8_t *, size_t);
