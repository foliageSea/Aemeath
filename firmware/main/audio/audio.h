#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"
#include "voice/protocol.h"
typedef struct { uint32_t epoch, turn, seq; uint16_t samples; float level; } audio_render_t;
esp_err_t audio_init(void);
// Capture task only; stereo microphones downmixed to 16k mono.
esp_err_t audio_capture(int16_t pcm[VOICE_FRAME_SAMPLES]);
// Voice worker only. Start mutes/flushes previous DMA before enabling EXIO3.
esp_err_t audio_play_start(uint32_t epoch, uint32_t turn);
void audio_play_abort(void);
bool audio_play_queue(const voice_packet_t *packet, int volume);
bool audio_render_poll(audio_render_t *render);
