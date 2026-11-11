#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"
#include "voice/protocol.h"
typedef struct { uint32_t epoch, turn, seq; uint16_t samples; float level; } audio_render_t;
esp_err_t audio_init(void);
// Capture task only; synchronized mono microphone + rendered reference through FD AEC.
esp_err_t audio_capture(int16_t pcm[VOICE_FRAME_SAMPLES]);
// Capture task only, on config/connection revision changes; discard old AEC output.
void audio_capture_reset(void);
// Voice worker only. Start mutes PA and begins a nonblocking DMA flush.
// Poll returns ESP_ERR_NOT_FINISHED until PA is ready; issue credits only on ESP_OK.
esp_err_t audio_play_start(uint32_t epoch, uint32_t turn);
esp_err_t audio_play_poll_start(void);
void audio_play_abort(void);
bool audio_play_queue(const voice_packet_t *packet, int volume);
bool audio_render_poll(audio_render_t *render);
