#include "background_format.h"
#include <string.h>

bool background_parse_etag(const char *text, uint64_t *out)
{
    if (!text || !out || *text++ != '"' || *text < '0' || *text > '9') return false;
    if (*text == '0' && text[1] != '"') return false;
    uint64_t n = 0;
    while (*text >= '0' && *text <= '9') {
        unsigned digit = (unsigned)(*text++ - '0');
        if (n > (UINT64_MAX - digit) / 10) return false;
        n = n * 10 + digit;
    }
    if (*text++ != '"' || *text) return false;
    *out = n;
    return true;
}
static int hex_digit(char c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}
bool background_parse_sha256(const char *text, uint8_t out[32])
{
    if (!text || strlen(text) != 64) return false;
    for (unsigned i = 0; i < 32; ++i) {
        int a = hex_digit(text[i * 2]), b = hex_digit(text[i * 2 + 1]);
        if (a < 0 || b < 0) return false;
        out[i] = (uint8_t)((a << 4) | b);
    }
    return true;
}
void background_hex_sha256(const uint8_t hash[32], char out[65])
{
    static const char digits[] = "0123456789abcdef";
    for (unsigned i = 0; i < 32; ++i) { out[i * 2] = digits[hash[i] >> 4]; out[i * 2 + 1] = digits[hash[i] & 15]; }
    out[64] = 0;
}
static unsigned be16(const uint8_t *p) { return (unsigned)p[0] << 8 | p[1]; }

bool background_jpeg_valid(const uint8_t *p, size_t len)
{
    if (!p || len < 4 || len > BACKGROUND_MAX_BYTES || p[0] != 0xff || p[1] != 0xd8) return false;
    size_t pos = 2;
    bool frame = false;
    unsigned channels = 0;
    uint8_t ids[3] = {0};
    while (pos < len) {
        if (p[pos++] != 0xff) return false;
        while (pos < len && p[pos] == 0xff) ++pos;
        if (pos >= len) return false;
        unsigned marker = p[pos++];
        // No nested SOI, premature EOI, standalone restart or arithmetic coding.
        if (marker == 0 || marker == 0xd8 || marker == 0xd9 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7)) return false;
        if (len - pos < 2) return false;
        unsigned n = be16(p + pos);
        if (n < 2 || n > len - pos) return false;
        const uint8_t *segment = p + pos;
        if (marker == 0xc0) {
            if (frame || n < 8 || segment[2] != 8 || be16(segment + 3) != BACKGROUND_HEIGHT || be16(segment + 5) != BACKGROUND_WIDTH) return false;
            channels = segment[7];
            if ((channels != 1 && channels != 3) || n != 8 + 3 * channels) return false;
            for (unsigned i = 0; i < channels; ++i) {
                ids[i] = segment[8 + i * 3];
                unsigned sampling = segment[9 + i * 3];
                if (!(sampling >> 4) || !(sampling & 15) || (sampling >> 4) > 4 || (sampling & 15) > 4 || segment[10 + i * 3] > 3) return false;
                for (unsigned j = 0; j < i; ++j) if (ids[i] == ids[j]) return false;
            }
            frame = true;
        } else if (marker == 0xda) {
            if (!frame || n != 6 + 2 * channels || segment[2] != channels) return false;
            for (unsigned i = 0; i < channels; ++i) if (segment[3 + 2 * i] != ids[i]) return false;
            if (segment[n - 3] != 0 || segment[n - 2] != 63 || segment[n - 1] != 0) return false;
            pos += n;
            // Entropy-coded bytes may contain FF00 or restart markers, but no
            // additional scan/frame. Require final EOI with no trailing body.
            while (pos < len) {
                if (p[pos++] != 0xff) continue;
                while (pos < len && p[pos] == 0xff) ++pos;
                if (pos >= len) return false;
                unsigned code = p[pos++];
                if (code == 0 || (code >= 0xd0 && code <= 0xd7)) continue;
                return code == 0xd9 && pos == len;
            }
            return false;
        } else if (!(marker == 0xc4 || marker == 0xdb || marker == 0xdd || marker == 0xfe || (marker >= 0xe0 && marker <= 0xef))) {
            return false; // Includes progressive/extended/lossless SOF and DNL.
        }
        pos += n;
    }
    return false;
}
