#ifndef AUDIO_TEST_HOST_H
#define AUDIO_TEST_HOST_H
#include <assert.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL -1
#define ESP_ERR_NO_MEM 1
#define ESP_ERR_INVALID_STATE 2
#define ESP_ERR_NOT_FINISHED 3
#define ESP_ERR_TIMEOUT 4
#define ESP_ERR_NOT_SUPPORTED 5
#define ESP_ERR_INVALID_SIZE 6
#define ESP_LOGI(tag, ...) do { (void)(tag); if (0) printf(__VA_ARGS__); } while (0)
#define ESP_LOGE(tag, ...) ESP_LOGI(tag, __VA_ARGS__)
static const char *esp_err_to_name(int error) { (void)error; return "host error"; }
static int64_t host_now;
static int64_t esp_timer_get_time(void) { return host_now; }

typedef int BaseType_t;
typedef int portMUX_TYPE;
#define portMUX_INITIALIZER_UNLOCKED 0
#define portENTER_CRITICAL(lock) ((void)(lock))
#define portEXIT_CRITICAL(lock) ((void)(lock))
#define portENTER_CRITICAL_ISR(lock) portENTER_CRITICAL(lock)
#define portEXIT_CRITICAL_ISR(lock) portEXIT_CRITICAL(lock)
#define pdTRUE 1
#define pdFALSE 0
#define pdMS_TO_TICKS(ms) (ms)
static unsigned host_waits, host_delays;
static void (*host_wait_hook)(void);
typedef struct { size_t capacity, size, head, count; unsigned char *data; } *QueueHandle_t;
static QueueHandle_t xQueueCreate(size_t capacity, size_t size)
{
    QueueHandle_t q = calloc(1, sizeof(*q));
    assert(q);
    q->data = calloc(capacity, size);
    assert(q->data);
    q->capacity = capacity; q->size = size;
    return q;
}
static QueueHandle_t xQueueCreateWithCaps(size_t capacity, size_t size, int caps)
{ (void)caps; return xQueueCreate(capacity, size); }
static int xQueueSend(QueueHandle_t q, const void *item, unsigned wait)
{
    assert(wait == 0);
    if (q->count == q->capacity) return pdFALSE;
    memcpy(q->data + ((q->head + q->count) % q->capacity) * q->size, item, q->size);
    ++q->count; return pdTRUE;
}
static int xQueueReceive(QueueHandle_t q, void *item, unsigned wait)
{
    if (!q->count && wait) {
        ++host_waits;
        if (host_wait_hook) host_wait_hook();
    }
    if (!q->count) return pdFALSE;
    memcpy(item, q->data + q->head * q->size, q->size);
    q->head = (q->head + 1) % q->capacity; --q->count; return pdTRUE;
}
static int xQueueSendFromISR(QueueHandle_t q, const void *item, BaseType_t *yield)
{ (void)yield; return xQueueSend(q, item, 0); }
static int xQueueReceiveFromISR(QueueHandle_t q, void *item, BaseType_t *yield)
{ (void)yield; return xQueueReceive(q, item, 0); }
static void xQueueReset(QueueHandle_t q) { q->head = q->count = 0; }
static void vQueueDelete(QueueHandle_t q) { free(q->data); free(q); }
#define vQueueDeleteWithCaps vQueueDelete
static void vTaskDelay(unsigned ticks) { (void)ticks; ++host_delays; }

#define MALLOC_CAP_SPIRAM 1
#define MALLOC_CAP_8BIT 2
#define MALLOC_CAP_INTERNAL 4
static void *heap_caps_calloc(size_t n, size_t size, int caps)
{ (void)caps; return calloc(n, size); }
static void *heap_caps_aligned_alloc(size_t alignment, size_t size, int caps)
{ (void)caps; return aligned_alloc(alignment, (size + alignment - 1) / alignment * alignment); }
static size_t heap_caps_get_free_size(int caps) { (void)caps; return 1000000; }

static bool host_pa;
static unsigned host_pa_on_calls, host_pa_off_calls;
static int host_pa_on_error, host_pa_off_error;
static esp_err_t board_audio_amp(bool enabled)
{
    if (enabled) {
        ++host_pa_on_calls;
        if (host_pa_on_error) return host_pa_on_error;
    } else {
        ++host_pa_off_calls;
        if (host_pa_off_error) return host_pa_off_error;
    }
    host_pa = enabled; return ESP_OK;
}
static void *board_i2c_bus(void) { return (void *)(uintptr_t)1; }
#define BOARD_I2C_HZ 400000
#define BOARD_I2S_MCLK 5
#define BOARD_I2S_BCLK 16
#define BOARD_I2S_WS 7
#define BOARD_I2S_DOUT 6
#define BOARD_I2S_DIN 15
#define GPIO_NUM_NC -1
#define I2C_NUM_0 0

typedef struct { void *dma_buf; size_t size; } i2s_event_data_t;
struct host_channel;
typedef struct host_channel *i2s_chan_handle_t;
typedef bool (*host_i2s_callback)(i2s_chan_handle_t, i2s_event_data_t *, void *);
typedef struct { host_i2s_callback on_sent, on_recv; } i2s_event_callbacks_t;
struct host_channel { i2s_event_callbacks_t callbacks; void *ctx; bool enabled; };
static struct host_channel host_tx, host_rx;
typedef struct { int dma_desc_num, dma_frame_num; bool auto_clear_after_cb, auto_clear_before_cb; } i2s_chan_config_t;
typedef struct { int clk_cfg, slot_cfg; struct { int mclk, bclk, ws, dout, din; } gpio_cfg; } i2s_std_config_t;
#define I2S_NUM_0 0
#define I2S_ROLE_MASTER 1
#define I2S_DATA_BIT_WIDTH_16BIT 16
#define I2S_SLOT_MODE_STEREO 2
#define I2S_CHANNEL_DEFAULT_CONFIG(port, role) ((i2s_chan_config_t){0})
#define I2S_STD_CLK_DEFAULT_CONFIG(rate) (rate)
#define I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(bits, mode) (bits)
static int i2s_new_channel(const i2s_chan_config_t *config, i2s_chan_handle_t *tx, i2s_chan_handle_t *rx)
{
    assert(config->dma_desc_num == 4 && config->dma_frame_num == 320);
    assert(!config->auto_clear_after_cb && !config->auto_clear_before_cb);
    memset(&host_tx, 0, sizeof(host_tx)); memset(&host_rx, 0, sizeof(host_rx));
    *tx = &host_tx; *rx = &host_rx; return ESP_OK;
}
static int i2s_channel_init_std_mode(i2s_chan_handle_t channel, const i2s_std_config_t *config)
{ (void)channel; assert(config->clk_cfg == 16000); return ESP_OK; }
static int i2s_channel_register_event_callback(i2s_chan_handle_t channel, const i2s_event_callbacks_t *callbacks, void *ctx)
{ channel->callbacks = *callbacks; channel->ctx = ctx; return ESP_OK; }
static int i2s_channel_enable(i2s_chan_handle_t channel) { channel->enabled = true; return ESP_OK; }
static int i2s_channel_disable(i2s_chan_handle_t channel) { channel->enabled = false; return ESP_OK; }
static int i2s_del_channel(i2s_chan_handle_t channel) { (void)channel; return ESP_OK; }

typedef struct { int bits_per_sample, channel, sample_rate, mclk_multiple; } esp_codec_dev_sample_info_t;
typedef struct audio_codec_if audio_codec_if_t;
struct audio_codec_if {
    int (*set_fs)(const audio_codec_if_t *, esp_codec_dev_sample_info_t *);
    int (*enable)(const audio_codec_if_t *, bool);
    int (*set_mic_gain)(const audio_codec_if_t *, float);
    int (*set_vol)(const audio_codec_if_t *, float);
};
typedef struct { int unused; } audio_codec_ctrl_if_t;
typedef struct { int unused; } audio_codec_gpio_if_t;
typedef struct { int port; void *bus_handle; int clock_speed_hz, addr; } audio_codec_i2c_cfg_t;
typedef struct { const audio_codec_ctrl_if_t *ctrl_if; const audio_codec_gpio_if_t *gpio_if; int codec_mode, pa_pin; bool use_mclk; struct { float pa_voltage, codec_dac_voltage; } hw_gain; } es8311_codec_cfg_t;
typedef struct { const audio_codec_ctrl_if_t *ctrl_if; int mic_selected; } es7210_codec_cfg_t;
#define ES8311_CODEC_DEFAULT_ADDR 0x18
#define ES7210_CODEC_DEFAULT_ADDR 0x40
#define ES7210_SEL_MIC1 1
#define ES7210_SEL_MIC2 2
#define ESP_CODEC_DEV_WORK_MODE_DAC 1
static int host_codec_fs(const audio_codec_if_t *codec, esp_codec_dev_sample_info_t *info)
{ (void)codec; assert(info->bits_per_sample == 16 && info->channel == 2 && info->sample_rate == 16000); return ESP_OK; }
static int host_codec_enable(const audio_codec_if_t *codec, bool enabled)
{ (void)codec; assert(enabled); return ESP_OK; }
static int host_codec_gain(const audio_codec_if_t *codec, float gain) { (void)codec; (void)gain; return ESP_OK; }
static const audio_codec_if_t host_codec = {host_codec_fs, host_codec_enable, host_codec_gain, host_codec_gain};
static const audio_codec_ctrl_if_t host_ctrl = {0};
static const audio_codec_gpio_if_t host_gpio = {0};
static const audio_codec_ctrl_if_t *audio_codec_new_i2c_ctrl(const audio_codec_i2c_cfg_t *config)
{ assert(config->bus_handle); return &host_ctrl; }
static const audio_codec_gpio_if_t *audio_codec_new_gpio(void) { return &host_gpio; }
static const audio_codec_if_t *es8311_codec_new(const es8311_codec_cfg_t *config)
{ assert(config->ctrl_if && config->gpio_if && config->use_mclk); return &host_codec; }
static const audio_codec_if_t *es7210_codec_new(const es7210_codec_cfg_t *config)
{ assert(config->ctrl_if && config->mic_selected == 3); return &host_codec; }
static void audio_codec_delete_codec_if(const audio_codec_if_t *codec) { (void)codec; }
static void audio_codec_delete_ctrl_if(const audio_codec_ctrl_if_t *ctrl) { (void)ctrl; }
static void audio_codec_delete_gpio_if(const audio_codec_gpio_if_t *gpio) { (void)gpio; }

typedef struct { int mic_num, ref_num, out_num, filter_length, sample_rate, caps, mode, nlp_level; } aec_config_t;
typedef struct { size_t frame_size; } aec_handle_t;
#define AEC_MODE_FD_HIGH_PERF 1
#define AEC_NLP_LEVEL_NORMAL 2
static size_t host_native_size = 512, host_aec_count, host_aec_calls;
static unsigned host_aec_creates, host_aec_destroys;
static int16_t host_aec_mic[65536], host_aec_ref[65536];
static bool host_aec_fail_create;
static void (*host_aec_create_hook)(void);
static aec_handle_t *aec_create_from_config(const aec_config_t *config)
{
    assert(config->mic_num == 1 && config->ref_num == 1 && config->out_num == 1);
    assert(config->sample_rate == 16000 && config->mode == AEC_MODE_FD_HIGH_PERF);
    if (host_aec_fail_create) return NULL;
    if (host_aec_create_hook) host_aec_create_hook();
    aec_handle_t *aec = malloc(sizeof(*aec)); assert(aec);
    aec->frame_size = host_native_size; ++host_aec_creates; return aec;
}
static void aec_destroy(aec_handle_t *aec) { ++host_aec_destroys; free(aec); }
static int aec_get_chunksize(aec_handle_t *aec) { return (int)aec->frame_size; }
static void aec_process(aec_handle_t *aec, int16_t *mic, int16_t *ref, int16_t *out)
{
    assert((uintptr_t)mic % 16 == 0 && (uintptr_t)ref % 16 == 0 && (uintptr_t)out % 16 == 0);
    assert(host_aec_count + aec->frame_size <= 65536);
    for (size_t i = 0; i < aec->frame_size; ++i) {
        host_aec_mic[host_aec_count + i] = mic[i];
        host_aec_ref[host_aec_count + i] = ref[i];
        out[i] = (int16_t)(mic[i] - ref[i] + 17);
        mic[i] = ref[i] = -123;
    }
    host_aec_count += aec->frame_size; ++host_aec_calls;
}
#endif
