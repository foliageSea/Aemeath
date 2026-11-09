#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#define VOICE_RATE 16000
#define VOICE_FRAME_SAMPLES 320
#define VOICE_HEADER_SIZE 20
#define VOICE_PCM_BYTES 640
#define VOICE_FRAME_BYTES 660
#define VOICE_PLAYBACK_FRAMES 32
#define VOICE_MAX_SAMPLES (VOICE_RATE * 120u)
typedef struct { uint32_t epoch, turn, seq; uint8_t kind; const uint8_t *pcm; size_t samples; } voice_packet_t;
typedef struct {
    uint32_t epoch, turn, seq, declared, received, rendered;
    unsigned outstanding;
    bool active, ended, started;
} voice_playback_t;
bool voice_packet_decode(const uint8_t *data, size_t len, voice_packet_t *out);
void voice_packet_header(uint8_t *out, uint8_t kind, uint32_t epoch, uint32_t turn, uint32_t seq);
bool voice_playback_start(voice_playback_t *p, uint32_t epoch, uint32_t turn, uint32_t samples);
bool voice_playback_receive(voice_playback_t *p, const voice_packet_t *frame);
bool voice_playback_end(voice_playback_t *p, uint32_t epoch, uint32_t turn);
bool voice_playback_render(voice_playback_t *p, uint32_t epoch, uint32_t turn, uint32_t seq, size_t samples);
bool voice_playback_done(const voice_playback_t *p);
