#include "background_codec.h"
#include "background_format.h"
#include <stdlib.h>
#include "esp_heap_caps.h"
#include "esp_jpeg_dec.h"

background_codec_result_t background_decode_jpeg(uint8_t *jpeg, size_t len, uint8_t **pixels)
{
    *pixels = NULL;
    if (!background_jpeg_valid(jpeg, len)) return BG_CODEC_INVALID;
    jpeg_dec_config_t config = DEFAULT_JPEG_DEC_CONFIG();
    config.output_type = JPEG_PIXEL_FORMAT_RGB565_LE;
    jpeg_dec_handle_t decoder = NULL;
    jpeg_error_t err = jpeg_dec_open(&config, &decoder);
    if (err != JPEG_ERR_OK) return err == JPEG_ERR_NO_MEM ? BG_CODEC_NO_MEMORY : BG_CODEC_INVALID;
    jpeg_dec_io_t io = {.inbuf = jpeg, .inbuf_len = (int)len};
    jpeg_dec_header_info_t header = {0};
    int output_bytes = 0;
    uint8_t *out = NULL;
    err = jpeg_dec_parse_header(decoder, &io, &header);
    if (err == JPEG_ERR_OK && header.width == BACKGROUND_WIDTH && header.height == BACKGROUND_HEIGHT) {
        err = jpeg_dec_get_outbuf_len(decoder, &output_bytes);
        if (err == JPEG_ERR_OK && output_bytes == (int)BACKGROUND_RGB_BYTES) {
            out = heap_caps_aligned_alloc(16, BACKGROUND_RGB_BYTES, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
            if (!out) err = JPEG_ERR_NO_MEM;
            else {
                io.outbuf = out;
                io.out_size = output_bytes;
                err = jpeg_dec_process(decoder, &io);
            }
        } else if (err == JPEG_ERR_OK) err = JPEG_ERR_BAD_DATA;
    } else if (err == JPEG_ERR_OK) err = JPEG_ERR_BAD_DATA;
    jpeg_dec_close(decoder);
    if (err != JPEG_ERR_OK) {
        free(out);
        return err == JPEG_ERR_NO_MEM ? BG_CODEC_NO_MEMORY : BG_CODEC_INVALID;
    }
    *pixels = out;
    return BG_CODEC_OK;
}
