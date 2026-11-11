// ES8311 DAC + ES7210 ADC share I2S0 clocks and the board's existing I2C bus.
// Codec interfaces configure analog hardware; direct IDF DMA owns PCM so its
// on_sent completion, rather than a write() return, is the rendered boundary.
#include "audio.h"
#include "echo_stream.h"
#include "esp_aec.h"
#include "esp_timer.h"
#include "board.h"
#include "driver/i2s_std.h"
#include "esp_codec_dev_defaults.h"
#include "esp_log.h"
#include "esp_heap_caps.h"
#include "freertos/idf_additions.h"

#if CONFIG_I2S_ISR_IRAM_SAFE
#error "Audio PCM queue uses PSRAM; I2S callbacks require enabled cache"
#endif
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include <stdlib.h>
#include <string.h>

#define DMA_BLOCKS 4
#define STEREO_SAMPLES (VOICE_FRAME_SAMPLES * 2)
typedef struct { audio_render_t render; uint32_t generation; int16_t pcm[STEREO_SAMPLES]; } block_t;
typedef struct { void *ptr; audio_render_t render; uint32_t generation; } slot_t;
static i2s_chan_handle_t s_tx, s_rx;
static const audio_codec_if_t *s_dac, *s_adc;
static const audio_codec_ctrl_if_t *s_dac_ctrl, *s_adc_ctrl;
static const audio_codec_gpio_if_t *s_gpio;
static QueueHandle_t s_pending, s_rendered, s_captured;
typedef struct { uint32_t seq; int64_t end_us; int16_t pcm[VOICE_FRAME_SAMPLES]; } capture_block_t;
static capture_block_t s_rx_previous, s_rx_discard;
static bool s_rx_previous_valid;
static uint32_t s_rx_seq;
static echo_reference_t *s_reference;
static echo_stream_t *s_echo;
static aec_handle_t *s_aec;
// Capture task owns alignment; ISR timestamps establish a fixed TX/RX frame offset.
static bool s_capture_started;
static uint32_t s_capture_next, s_reference_offset;
static int64_t s_capture_end_us, s_capture_min_end_us;
// Voice worker owns startup. PA state is shared with the reference ISR.
static bool s_start_pending, s_amp_enabled, s_amp_previous;
static uint32_t s_start_cycles;
static int64_t s_start_deadline, s_amp_changed_us;
static esp_err_t s_amp_stop_error;
static int16_t s_reference_mono[VOICE_FRAME_SAMPLES];
static portMUX_TYPE s_lock = portMUX_INITIALIZER_UNLOCKED;
static uint32_t s_generation=1, s_cycles;
static bool s_ready, s_event_overflow;
static slot_t s_slots[DMA_BLOCKS];
// Only the I2S ISR touches this staging block; keep its large copy off ISR stack.
static block_t s_next;

static bool sent(i2s_chan_handle_t handle, i2s_event_data_t *event, void *ctx)
{
    (void)handle; (void)ctx;
    BaseType_t yield=pdFALSE;
    portENTER_CRITICAL_ISR(&s_lock);
    uint32_t sequence=s_cycles++;
    int64_t end_us=esp_timer_get_time();
    if (event->size==sizeof(s_next.pcm)) {
        // Read the buffer that JUST rendered, before it is refilled. Software
        // volume and DMA underrun silence are already reflected in these samples.
        const int16_t *played=event->dma_buf;
        int64_t change=s_amp_changed_us-(end_us-20000);
        int transition=change<=0?0:(change>=20000?VOICE_FRAME_SAMPLES:
            (int)((change*VOICE_RATE+999999)/1000000));
        for (int i=0;i<VOICE_FRAME_SAMPLES;i++) {
            bool audible=i>=transition?s_amp_enabled:s_amp_previous;
            s_reference_mono[i]=audible?played[2*i]:0;
        }
        echo_reference_push(s_reference,s_reference_mono,sequence,end_us);
    }
    slot_t *slot=NULL;
    for (int i=0;i<DMA_BLOCKS;i++) if (s_slots[i].ptr==event->dma_buf) slot=&s_slots[i];
    if (!slot) for (int i=0;i<DMA_BLOCKS;i++) if (!s_slots[i].ptr) { slot=&s_slots[i]; slot->ptr=event->dma_buf; break; }
    if (slot && slot->render.samples && slot->generation==s_generation) {
        if (xQueueSendFromISR(s_rendered,&slot->render,&yield)!=pdTRUE) s_event_overflow=true;
    }
    if (slot) slot->render.samples=0;
    // This callback is AFTER the old buffer rendered. Fill the released buffer
    // for its next lap; no i2s_channel_write may run on this TX channel.
    bool got=false;
    while (xQueueReceiveFromISR(s_pending,&s_next,&yield)==pdTRUE) {
        if (s_next.generation==s_generation) { got=true; break; }
    }
    if (got && slot && event->size==sizeof(s_next.pcm)) {
        memcpy(event->dma_buf,s_next.pcm,event->size);
        slot->render=s_next.render; slot->generation=s_generation;
    } else memset(event->dma_buf,0,event->size);
    portEXIT_CRITICAL_ISR(&s_lock);
    return yield==pdTRUE;
}

// Copy RX DMA in its callback: pairing must follow capture time, not when a task
// happens to read a buffered I2S frame. Delay delivery by one frame so the matching
// TX completion is available regardless of the order of the two interrupts.
static bool received(i2s_chan_handle_t handle, i2s_event_data_t *event, void *ctx)
{
    (void)handle; (void)ctx;
    BaseType_t yield=pdFALSE;
    if (event->size!=sizeof(s_next.pcm)) return false;
    if (s_rx_previous_valid && xQueueSendFromISR(s_captured,&s_rx_previous,&yield)!=pdTRUE) {
        // Retain the latest bounded audio while capture is disabled. A running
        // consumer detects the resulting sequence gap and fails the session.
        xQueueReceiveFromISR(s_captured,&s_rx_discard,&yield);
        xQueueSendFromISR(s_captured,&s_rx_previous,&yield);
    }
    s_rx_previous.seq=s_rx_seq++;
    s_rx_previous.end_us=esp_timer_get_time();
    const int16_t *stereo=event->dma_buf;
    for (int i=0;i<VOICE_FRAME_SAMPLES;i++)
        s_rx_previous.pcm[i]=(int16_t)(((int32_t)stereo[2*i]+stereo[2*i+1])/2);
    s_rx_previous_valid=true;
    return yield==pdTRUE;
}

static void echo_process(void *ctx, int16_t *mic, int16_t *ref, int16_t *out);

static esp_err_t echo_reset(void)
{
    if (s_aec) aec_destroy(s_aec);
    aec_config_t config={.mic_num=1,.ref_num=1,.out_num=1,.filter_length=4,
        .sample_rate=VOICE_RATE,.caps=MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT,
        .mode=AEC_MODE_FD_HIGH_PERF,.nlp_level=AEC_NLP_LEVEL_NORMAL};
    s_aec=aec_create_from_config(&config);
    if (!s_aec) return ESP_ERR_NO_MEM;
    if (!echo_stream_init(s_echo,aec_get_chunksize(s_aec),echo_process,s_aec)) return ESP_ERR_NOT_SUPPORTED;
    return ESP_OK;
}

static void echo_process(void *ctx, int16_t *mic, int16_t *ref, int16_t *out)
{
    aec_process(ctx,mic,ref,out);
}

static void cleanup(void)
{
    board_audio_amp(false);
    if (s_tx) { i2s_channel_disable(s_tx); i2s_del_channel(s_tx); s_tx=NULL; }
    if (s_rx) { i2s_channel_disable(s_rx); i2s_del_channel(s_rx); s_rx=NULL; }
    if (s_dac) { audio_codec_delete_codec_if(s_dac); s_dac=NULL; }
    if (s_adc) { audio_codec_delete_codec_if(s_adc); s_adc=NULL; }
    if (s_dac_ctrl) { audio_codec_delete_ctrl_if(s_dac_ctrl); s_dac_ctrl=NULL; }
    if (s_adc_ctrl) { audio_codec_delete_ctrl_if(s_adc_ctrl); s_adc_ctrl=NULL; }
    if (s_gpio) { audio_codec_delete_gpio_if(s_gpio); s_gpio=NULL; }
    if (s_captured) { vQueueDeleteWithCaps(s_captured); s_captured=NULL; }
    if (s_aec) { aec_destroy(s_aec); s_aec=NULL; }
    free(s_reference); s_reference=NULL; free(s_echo); s_echo=NULL;
    s_rx_previous_valid=false; s_rx_seq=0; s_capture_started=false;
    s_start_pending=false; s_amp_enabled=s_amp_previous=false; s_amp_changed_us=0;
    if (s_pending) { vQueueDeleteWithCaps(s_pending); s_pending=NULL; }
    if (s_rendered) { vQueueDelete(s_rendered); s_rendered=NULL; }
    // A failed codec probe can be retried on the next config; no descriptor
    // address or queued-completion flag may survive channel destruction.
    memset(s_slots,0,sizeof(s_slots));
    memset(&s_next,0,sizeof(s_next));
    s_cycles=0; s_event_overflow=false; s_ready=false;
}

esp_err_t audio_init(void)
{
    if (s_ready) return ESP_OK;
    esp_err_t err=ESP_ERR_NO_MEM;
    // PCM staging is CPU-copy data, not a DMA source. Keep its ~42 KiB in
    // PSRAM, reserving internal memory for actual I2S DMA buffers/descriptors.
    // The non-IRAM-safe I2S interrupt is masked whenever cache is unavailable.
    s_pending=xQueueCreateWithCaps(VOICE_PLAYBACK_FRAMES,sizeof(block_t),MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT);
    s_rendered=xQueueCreate(VOICE_PLAYBACK_FRAMES,sizeof(audio_render_t));
    s_captured=xQueueCreateWithCaps(8,sizeof(capture_block_t),MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT);
    s_reference=heap_caps_calloc(1,sizeof(*s_reference),MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT);
    s_echo=heap_caps_aligned_alloc(16,sizeof(*s_echo),MALLOC_CAP_SPIRAM|MALLOC_CAP_8BIT);
    if (!s_pending || !s_rendered || !s_captured || !s_reference || !s_echo) goto fail;
    if ((err=echo_reset())!=ESP_OK) goto fail;
    i2s_chan_config_t chan=I2S_CHANNEL_DEFAULT_CONFIG(I2S_NUM_0,I2S_ROLE_MASTER);
    chan.dma_desc_num=DMA_BLOCKS; chan.dma_frame_num=VOICE_FRAME_SAMPLES;
    // Our callback supplies/clears released DMA buffers itself.
    chan.auto_clear_after_cb=false; chan.auto_clear_before_cb=false;
    if ((err=i2s_new_channel(&chan,&s_tx,&s_rx))!=ESP_OK) goto fail;
    i2s_std_config_t cfg={
        .clk_cfg=I2S_STD_CLK_DEFAULT_CONFIG(VOICE_RATE),
        .slot_cfg=I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(I2S_DATA_BIT_WIDTH_16BIT,I2S_SLOT_MODE_STEREO),
        .gpio_cfg={.mclk=BOARD_I2S_MCLK,.bclk=BOARD_I2S_BCLK,.ws=BOARD_I2S_WS,.dout=BOARD_I2S_DOUT,.din=BOARD_I2S_DIN},
    };
    if ((err=i2s_channel_init_std_mode(s_tx,&cfg))!=ESP_OK || (err=i2s_channel_init_std_mode(s_rx,&cfg))!=ESP_OK) goto fail;
    i2s_event_callbacks_t callbacks={.on_sent=sent};
    if ((err=i2s_channel_register_event_callback(s_tx,&callbacks,NULL))!=ESP_OK) goto fail;
    callbacks=(i2s_event_callbacks_t){.on_recv=received};
    if ((err=i2s_channel_register_event_callback(s_rx,&callbacks,NULL))!=ESP_OK) goto fail;
    if ((err=i2s_channel_enable(s_tx))!=ESP_OK || (err=i2s_channel_enable(s_rx))!=ESP_OK) goto fail;
    audio_codec_i2c_cfg_t ctrl={.port=I2C_NUM_0,.bus_handle=board_i2c_bus(),.clock_speed_hz=BOARD_I2C_HZ,.addr=ES8311_CODEC_DEFAULT_ADDR};
    s_dac_ctrl=audio_codec_new_i2c_ctrl(&ctrl); ctrl.addr=ES7210_CODEC_DEFAULT_ADDR;
    s_adc_ctrl=audio_codec_new_i2c_ctrl(&ctrl); s_gpio=audio_codec_new_gpio();
    err=ESP_ERR_NO_MEM;
    if (!s_dac_ctrl || !s_adc_ctrl || !s_gpio) goto fail;
    es8311_codec_cfg_t dac={.ctrl_if=s_dac_ctrl,.gpio_if=s_gpio,.codec_mode=ESP_CODEC_DEV_WORK_MODE_DAC,.pa_pin=GPIO_NUM_NC,.use_mclk=true,.hw_gain={.pa_voltage=5.0,.codec_dac_voltage=3.3}};
    es7210_codec_cfg_t adc={.ctrl_if=s_adc_ctrl,.mic_selected=ES7210_SEL_MIC1|ES7210_SEL_MIC2};
    s_dac=es8311_codec_new(&dac); s_adc=es7210_codec_new(&adc);
    if (!s_dac || !s_adc) goto fail;
    esp_codec_dev_sample_info_t fs={.bits_per_sample=16,.channel=2,.sample_rate=VOICE_RATE,.mclk_multiple=256};
    if ((err=s_dac->set_fs(s_dac,&fs))!=ESP_OK || (err=s_adc->set_fs(s_adc,&fs))!=ESP_OK ||
        (err=s_dac->enable(s_dac,true))!=ESP_OK || (err=s_adc->enable(s_adc,true))!=ESP_OK ||
        (err=s_adc->set_mic_gain(s_adc,24))!=ESP_OK || (err=s_dac->set_vol(s_dac,-6))!=ESP_OK) goto fail;
    s_ready=true;
    ESP_LOGI("audio","16k duplex + FD AEC (%d samples), ES7210 MIC1/2 -> mono, ES8311 DAC; free internal %u, PSRAM %u",
        aec_get_chunksize(s_aec),
        (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL|MALLOC_CAP_8BIT),
        (unsigned)heap_caps_get_free_size(MALLOC_CAP_SPIRAM));
    return ESP_OK;
fail:
    cleanup(); return err;
}

void audio_capture_reset(void) { s_capture_started=false; }

esp_err_t audio_capture(int16_t pcm[VOICE_FRAME_SAMPLES])
{
    if (!s_ready) return ESP_ERR_INVALID_STATE;
    // Starting/resuming capture discards disabled-period audio and rebuilds the
    // filter. During uninterrupted capture every RX frame must be contiguous.
    if (!s_capture_started) {
        s_capture_started=false;
        esp_err_t err=echo_reset();
        if (err!=ESP_OK) return err;
        s_capture_min_end_us=esp_timer_get_time()+20000;
        xQueueReset(s_captured);
    }
    while (!echo_stream_read(s_echo,pcm,VOICE_FRAME_SAMPLES)) {
        capture_block_t block; int16_t reference[VOICE_FRAME_SAMPLES];
        if (xQueueReceive(s_captured,&block,pdMS_TO_TICKS(100))!=pdTRUE) {
            s_capture_started=false; return ESP_ERR_TIMEOUT;
        }
        // The RX ISR holds one frame for pairing. Its first delivery after a
        // reset may still precede the new connection/configuration revision.
        if (block.end_us<s_capture_min_end_us) continue;
        int64_t interval=block.end_us-s_capture_end_us;
        if (s_capture_started && (block.seq!=s_capture_next || interval<10000 || interval>30000)) {
            s_capture_started=false; return ESP_ERR_INVALID_SIZE;
        }
        portENTER_CRITICAL(&s_lock);
        uint32_t matched=0;
        bool aligned=echo_reference_find(s_reference,block.end_us,&matched);
        if (!s_capture_started && aligned) s_reference_offset=matched-block.seq;
        // Callback counts alone cannot detect a hardware EOF skipped while IRQs
        // are masked. Recheck the paired timestamp on every frame without
        // silently changing the established offset or adapting on wrong audio.
        aligned=aligned && matched==block.seq+s_reference_offset;
        const int16_t *ref=aligned?echo_reference_lookup(s_reference,matched):NULL;
        if (ref) memcpy(reference,ref,sizeof(reference));
        portEXIT_CRITICAL(&s_lock);
        // Never feed an unsynchronized microphone/reference pair or silently
        // fall back to raw microphone audio during playback.
        if (!ref) { s_capture_started=false; return ESP_ERR_INVALID_STATE; }
        s_capture_started=true; s_capture_next=block.seq+1; s_capture_end_us=block.end_us;
        if (!echo_stream_feed(s_echo,block.pcm,reference,VOICE_FRAME_SAMPLES)) {
            s_capture_started=false; return ESP_ERR_INVALID_SIZE;
        }
    }
    return ESP_OK;
}
void audio_play_abort(void)
{
    if (!s_ready) return;
    s_start_pending=false;
    s_amp_stop_error=board_audio_amp(false);
    portENTER_CRITICAL(&s_lock);
    if (s_amp_stop_error==ESP_OK && s_amp_enabled) {
        s_amp_previous=s_amp_enabled; s_amp_enabled=false; s_amp_changed_us=esp_timer_get_time();
    }
    s_generation++; s_event_overflow=false;
    portEXIT_CRITICAL(&s_lock);
    xQueueReset(s_pending); xQueueReset(s_rendered);
    if (s_amp_stop_error!=ESP_OK) ESP_LOGE("audio","PA mute failed: %s",esp_err_to_name(s_amp_stop_error));
}
esp_err_t audio_play_start(uint32_t epoch, uint32_t turn)
{
    if (!s_ready || !epoch || !turn) return ESP_ERR_INVALID_STATE;
    audio_play_abort();
    if (s_amp_stop_error!=ESP_OK) return s_amp_stop_error;
    portENTER_CRITICAL(&s_lock); s_start_cycles=s_cycles; portEXIT_CRITICAL(&s_lock);
    s_start_deadline=esp_timer_get_time()+200000;
    s_start_pending=true;
    return ESP_OK;
}
esp_err_t audio_play_poll_start(void)
{
    if (!s_ready || !s_start_pending) return ESP_ERR_INVALID_STATE;
    uint32_t cycles; portENTER_CRITICAL(&s_lock); cycles=s_cycles; portEXIT_CRITICAL(&s_lock);
    if (cycles-s_start_cycles<DMA_BLOCKS) {
        if (esp_timer_get_time()<s_start_deadline) return ESP_ERR_NOT_FINISHED;
        s_start_pending=false; return ESP_ERR_TIMEOUT;
    }
    // No blocking DMA wait: the voice worker can service cancel before this poll.
    s_start_pending=false;
    esp_err_t err=board_audio_amp(true);
    if (err==ESP_OK) {
        portENTER_CRITICAL(&s_lock);
        s_amp_previous=s_amp_enabled; s_amp_enabled=true; s_amp_changed_us=esp_timer_get_time();
        portEXIT_CRITICAL(&s_lock);
    }
    return err;
}

bool audio_play_queue(const voice_packet_t *p, int volume)
{
    if (!s_ready) return false;
    if (volume<0) volume=0;
    if (volume>100) volume=100;
    block_t block={.render={.epoch=p->epoch,.turn=p->turn,.seq=p->seq,.samples=p->samples}};
    portENTER_CRITICAL(&s_lock); block.generation=s_generation; portEXIT_CRITICAL(&s_lock);
    uint32_t peak=0;
    for (size_t i=0;i<p->samples;i++) {
        int16_t raw=(int16_t)((uint16_t)p->pcm[2*i]|(uint16_t)p->pcm[2*i+1]<<8);
        int32_t sample=(int32_t)raw*volume/100;
        block.pcm[2*i]=block.pcm[2*i+1]=(int16_t)sample;
        uint32_t abs_sample=sample<0?-sample:sample; if (abs_sample>peak) peak=abs_sample;
    }
    block.render.level=peak/32768.0f;
    return xQueueSend(s_pending,&block,0)==pdTRUE;
}
bool audio_render_poll(audio_render_t *r)
{
    if (!s_ready) return false;
    portENTER_CRITICAL(&s_lock); bool overflow=s_event_overflow; portEXIT_CRITICAL(&s_lock);
    if (overflow) { *r=(audio_render_t){0}; return true; }
    return xQueueReceive(s_rendered,r,0)==pdTRUE;
}
