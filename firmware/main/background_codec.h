#pragma once
#include "background_store.h"

typedef enum { BG_CODEC_OK, BG_CODEC_INVALID, BG_CODEC_NO_MEMORY } background_codec_result_t;
// On success caller owns *pixels and frees it with free(). No LVGL operations.
background_codec_result_t background_decode_jpeg(uint8_t *, size_t, uint8_t **pixels);
