// ES8311 DAC + ES7210 ADC share I2S0 clocks and the board's existing I2C bus.
// Codec interfaces configure analog hardware; direct IDF DMA owns PCM so its
// on_sent completion, rather than a write() return, is the rendered boundary.
#include "audio.h"
#include "board.h"
#include "driver/i2s_std.h"
#include "esp_codec_dev_defaults.h"
#include "esp_log.h"
#include "esp_heap_caps.h"
#include "esp_timer.h"
#include "freertos/idf_additions.h"

#if CONFIG_I2S_ISR_IRAM_SAFE
#error "Audio PCM queue uses PSRAM; I2S callbacks require enabled cache"
#endif
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include <math.h>
#include <string.h>

#define DMA_BLOCKS 4
#define STEREO_SAMPLES (VOICE_FRAME_SAMPLES * 2)
typedef struct { audio_render_t render; uint32_t generation; int16_t pcm[STEREO_SAMPLES]; } block_t;
typedef struct { void *ptr; audio_render_t render; uint32_t generation; } slot_t;
static i2s_chan_handle_t s_tx, s_rx;
static const audio_codec_if_t *s_dac, *s_adc;
static const audio_codec_ctrl_if_t *s_dac_ctrl, *s_adc_ctrl;
static const audio_codec_gpio_if_t *s_gpio;
static QueueHandle_t s_pending, s_rendered;
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
    s_cycles++;
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
    if (!s_pending || !s_rendered) goto fail;
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
    ESP_LOGI("audio","16k stereo duplex I2S0, ES7210 MIC1/2 -> mono, ES8311 DAC; EXIO3 amp gated; free internal %u, PSRAM %u",
        (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL|MALLOC_CAP_8BIT),
        (unsigned)heap_caps_get_free_size(MALLOC_CAP_SPIRAM));
    return ESP_OK;
fail:
    cleanup(); return err;
}

esp_err_t audio_capture(int16_t pcm[VOICE_FRAME_SAMPLES])
{
    if (!s_ready) return ESP_ERR_INVALID_STATE;
    int16_t stereo[STEREO_SAMPLES]; size_t n=0;
    esp_err_t err=i2s_channel_read(s_rx,stereo,sizeof(stereo),&n,100);
    if (err!=ESP_OK || n!=sizeof(stereo)) return err==ESP_OK?ESP_ERR_INVALID_SIZE:err;
    for (int i=0;i<VOICE_FRAME_SAMPLES;i++) pcm[i]=(int16_t)(((int32_t)stereo[2*i]+stereo[2*i+1])/2);
    return ESP_OK;
}
void audio_play_abort(void)
{
    if (!s_ready) return;
    board_audio_amp(false);
    portENTER_CRITICAL(&s_lock); s_generation++; s_event_overflow=false; portEXIT_CRITICAL(&s_lock);
    xQueueReset(s_pending); xQueueReset(s_rendered);
}
static esp_err_t output_restart(void)
{
    audio_play_abort();
    uint32_t begin;
    portENTER_CRITICAL(&s_lock); begin=s_cycles; portEXIT_CRITICAL(&s_lock);
    // Observe actual DMA laps: all descriptors must have been cleared while
    // PA is off before accepting the next turn. RX clocks remain running.
    for (int i=0;i<40;i++) {
        uint32_t cycles; portENTER_CRITICAL(&s_lock); cycles=s_cycles; portEXIT_CRITICAL(&s_lock);
        if (cycles-begin>=DMA_BLOCKS) return board_audio_amp(true);
        vTaskDelay(pdMS_TO_TICKS(5));
    }
    return ESP_ERR_TIMEOUT;
}
esp_err_t audio_play_start(uint32_t epoch, uint32_t turn)
{
    if (!s_ready || !epoch || !turn) return ESP_ERR_INVALID_STATE;
    return output_restart();
}

// Ready cue: a short rising two-note chime. Its blocks carry render.samples=0,
// so the TX ISR never reports them as rendered turn audio.
#define CUE_TAIL_MS 150 // speaker decay/room echo still muted on the mic path
static const struct { float hz; int blocks; } CUE_NOTES[]={{880.0f,4},{1318.5f,6}};
static block_t s_cue_block; // voice worker only; too large for its stack
static uint32_t s_cue_generation;
static int64_t s_cue_amp_off, s_cue_mute_until;

esp_err_t audio_cue_ready(int volume)
{
    if (!s_ready) return ESP_ERR_INVALID_STATE;
    if (volume<=0) return ESP_OK;
    if (volume>100) volume=100;
    // Gate the microphone from the moment the cue is requested; the restart
    // below may take a few DMA laps before the chime actually starts.
    int64_t now=esp_timer_get_time();
    portENTER_CRITICAL(&s_lock); s_cue_generation=s_generation+1; s_cue_mute_until=now+1000000; portEXIT_CRITICAL(&s_lock);
    esp_err_t err=output_restart();
    if (err!=ESP_OK) { portENTER_CRITICAL(&s_lock); s_cue_mute_until=0; portEXIT_CRITICAL(&s_lock); return err; }
    uint32_t generation; portENTER_CRITICAL(&s_lock); generation=s_generation; portEXIT_CRITICAL(&s_lock);
    const float amplitude=9000.0f*volume/100.0f;
    const int fade=VOICE_RATE/200; // 5 ms raised-cosine edges avoid clicks
    int queued=0;
    for (size_t n=0;n<sizeof(CUE_NOTES)/sizeof(CUE_NOTES[0]);n++) {
        const int total=CUE_NOTES[n].blocks*VOICE_FRAME_SAMPLES;
        const float step=2.0f*(float)M_PI*CUE_NOTES[n].hz/VOICE_RATE;
        for (int b=0;b<CUE_NOTES[n].blocks;b++) {
            memset(&s_cue_block,0,sizeof(s_cue_block));
            s_cue_block.generation=generation;
            for (int i=0;i<VOICE_FRAME_SAMPLES;i++) {
                int k=b*VOICE_FRAME_SAMPLES+i, edge=k<total-1-k?k:total-1-k;
                float env=edge<fade?0.5f-0.5f*cosf((float)M_PI*edge/fade):1.0f;
                // Let the second note decay instead of stopping flat.
                if (n==sizeof(CUE_NOTES)/sizeof(CUE_NOTES[0])-1) env*=1.0f-0.6f*k/total;
                int16_t sample=(int16_t)(amplitude*env*sinf(step*k));
                s_cue_block.pcm[2*i]=s_cue_block.pcm[2*i+1]=sample;
            }
            if (xQueueSend(s_pending,&s_cue_block,0)!=pdTRUE) break;
            queued++;
        }
    }
    // Queued blocks start after the DMA ring's already-armed buffers drain.
    int64_t done=esp_timer_get_time()+(int64_t)(queued+DMA_BLOCKS)*VOICE_FRAME_SAMPLES*1000000/VOICE_RATE;
    portENTER_CRITICAL(&s_lock);
    s_cue_generation=generation; s_cue_amp_off=done; s_cue_mute_until=done+CUE_TAIL_MS*1000;
    portEXIT_CRITICAL(&s_lock);
    return ESP_OK;
}
void audio_cue_poll(void)
{
    if (!s_ready) return;
    portENTER_CRITICAL(&s_lock);
    bool off=s_cue_amp_off && esp_timer_get_time()>=s_cue_amp_off;
    bool mine=s_cue_generation==s_generation;
    if (off) s_cue_amp_off=0;
    portEXIT_CRITICAL(&s_lock);
    // A playback start after the cue owns the amplifier (new generation).
    if (off && mine) board_audio_amp(false);
}
bool audio_cue_active(void)
{
    portENTER_CRITICAL(&s_lock);
    bool active=s_cue_mute_until && s_cue_generation==s_generation && esp_timer_get_time()<s_cue_mute_until;
    portEXIT_CRITICAL(&s_lock);
    return active;
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
