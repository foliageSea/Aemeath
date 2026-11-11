#include "voice.h"
#include "protocol.h"
#include "audio/audio.h"
#include "net/voice_ws.h"
#include "ui/voice_page.h"
#include "ui/dashboard.h"
#include "cJSON.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef enum { RX_TEXT, RX_BINARY, CMD_WAKE, CMD_CANCEL, CMD_CLEAR, CMD_MUTE } event_kind_t;
#define CONTROL_DEPTH 8
typedef struct { uint32_t epoch; uint64_t order; event_kind_t kind; uint8_t *data; size_t len; bool muted; } event_t;
static QueueHandle_t s_events, s_text, s_controls;
// Text is inspected for cancel on the worker, never on HTTPD. Non-cancel text
// is merged with binary by order so playback_end cannot overtake its PCM.
static event_t s_pending[CONTROL_DEPTH];
static size_t s_pending_count;
static uint64_t s_receive_order, s_discard_through;
static portMUX_TYPE s_lock=portMUX_INITIALIZER_UNLOCKED;
static bool s_capture_enabled, s_muted, s_audio_ready, s_mic_meter;
static uint32_t s_capture_epoch, s_capture_revision;
// Everything below, except the capture snapshot and event queue, is worker-owned.
static uint32_t s_epoch, s_turn, s_closed_turn;
static voice_playback_t s_play;
static bool s_start_pending;
static int s_volume=70;
static int64_t s_error_until, s_play_deadline;
static char s_phase[24]="idle";

static void capture_snapshot(bool enabled, uint32_t epoch)
{
    portENTER_CRITICAL(&s_lock);
    if (s_capture_enabled!=enabled || s_capture_epoch!=epoch) s_capture_revision++;
    s_capture_enabled=enabled; s_capture_epoch=epoch;
    portEXIT_CRITICAL(&s_lock);
}
static void playback_activity(void)
{
    s_play_deadline=esp_timer_get_time()+(int64_t)voice_playback_idle_ms(&s_play)*1000;
}
static void ui(const char *phase, const char *text)
{
    strlcpy(s_phase,phase,sizeof(s_phase));
    portENTER_CRITICAL(&s_lock); s_mic_meter=!strcmp(phase,"listening"); portEXIT_CRITICAL(&s_lock);
    if (!strcmp(phase,"wake") || !strcmp(phase,"listening")) dashboard_voice_begin();
    voice_page_update(phase,text,s_turn);
    if (!strcmp(phase,"idle")) dashboard_voice_finish();
}
static bool send_json(const char *type, uint32_t turn, const char *extra)
{
    char out[512];
    int n=snprintf(out,sizeof(out),"{\"v\":1,\"type\":\"%s\",\"epoch\":%"PRIu32",\"turn\":%"PRIu32"%s}",type,s_epoch,turn,extra?extra:"");
    if (n<=0 || n>=sizeof(out)) return false;
    return !strcmp(type,"wake")?voice_ws_send_ordered(s_epoch,out,n):voice_ws_send(s_epoch,false,out,n);
}
static void abort_round(bool finish)
{
    audio_play_abort(); s_start_pending=false;
    memset(&s_play,0,sizeof(s_play)); s_play_deadline=0;
    voice_page_level(0);
    if (s_turn>s_closed_turn) s_closed_turn=s_turn;
    if (finish) { s_error_until=0; ui("idle",""); }
}
static void fail_round(const char *code, const char *message)
{
    abort_round(false);
    // Both strings are internal constants; never interpolate remote text as JSON.
    char extra[240]; snprintf(extra,sizeof(extra),",\"code\":\"%s\",\"message\":\"%s\"",code,message);
    send_json("error",s_turn,extra);
    ui("error",message); s_error_until=esp_timer_get_time()+5000000;
}
static void reset_connection(uint32_t epoch)
{
    capture_snapshot(false,0); abort_round(true);
    s_epoch=epoch; s_turn=s_closed_turn=0;
    portENTER_CRITICAL(&s_lock); s_muted=false; portEXIT_CRITICAL(&s_lock);
    voice_page_set_muted(false);
    if (epoch) send_json("hello",0,",\"sample_rate\":16000,\"channels\":1,\"encoding\":\"pcm_s16le\",\"frame_samples\":320,\"capabilities\":[\"capture\",\"playback\",\"avatar\",\"stream_playback\"]");
}
static bool number(const cJSON *j, const char *key, uint32_t *out)
{
    const cJSON *item=cJSON_GetObjectItemCaseSensitive(j,key);
    if (!cJSON_IsNumber(item) || !(item->valuedouble>=0 && item->valuedouble<=UINT32_MAX)) return false;
    uint32_t n=(uint32_t)item->valuedouble;
    if ((double)n!=item->valuedouble) return false;
    *out=n; return true;
}
static const char *string(const cJSON *j, const char *key)
{
    const cJSON *item=cJSON_GetObjectItemCaseSensitive(j,key);
    return cJSON_IsString(item)?item->valuestring:NULL;
}
static bool bounded_json(const uint8_t *data, size_t len)
{
    // cJSON is recursive. Bound nesting before parsing untrusted socket input.
    unsigned depth=0; bool quoted=false, escaped=false;
    for (size_t i=0;i<len;i++) {
        unsigned char c=data[i]; if (!c) return false;
        if (quoted) {
            if (escaped) {
                // cJSON strings are NUL terminated; reject decoded NUL aliases.
                if (c=='u' && i+4<len && !memcmp(data+i+1,"0000",4)) return false;
                escaped=false;
            } else if (c=='\\') escaped=true; else if (c=='"') quoted=false;
        }
        else if (c=='"') quoted=true;
        else if (c=='{' || c=='[') { if (++depth>8) return false; }
        else if (c=='}' || c==']') { if (!depth) return false; depth--; }
    }
    return !quoted && !depth;
}
static cJSON *parse_message(const uint8_t *data, size_t len)
{
    if (!data || !len || len>4096 || !bounded_json(data,len)) return NULL;
    const char *end=NULL;
    cJSON *j=cJSON_ParseWithLengthOpts((const char *)data,len+1,&end,true);
    if (!j || !cJSON_IsObject(j) || end!=(const char *)data+len) { cJSON_Delete(j); return NULL; }
    // Ambiguous envelope fields must never authorize priority cancellation.
    const char *keys[]={"v","type","epoch","turn"};
    for (size_t k=0;k<sizeof(keys)/sizeof(keys[0]);k++) {
        unsigned count=0;
        for (const cJSON *item=j->child;item;item=item->next)
            if (item->string && !strcmp(item->string,keys[k])) count++;
        if (count>1) { cJSON_Delete(j); return NULL; }
    }
    return j;
}
static bool priority_cancel(const event_t *event)
{
    cJSON *j=parse_message(event->data,event->len);
    uint32_t v, epoch, turn;
    const char *type=j?string(j,"type"):NULL;
    bool valid=type && !strcmp(type,"cancel") && number(j,"v",&v) && v==1 &&
        number(j,"epoch",&epoch) && epoch==event->epoch && number(j,"turn",&turn);
    if (valid && event->order>s_discard_through && epoch==s_epoch && turn>=s_turn && (!turn || turn>s_closed_turn)) {
        // A cancel may overtake its own queued start. Seal that turn as well as
        // the current playback; delayed state/start/PCM must not resurrect it.
        s_turn=turn; abort_round(true);
    }
    cJSON_Delete(j);
    return valid;
}
static void text_message(const uint8_t *data, size_t len, bool discard_round)
{
    cJSON *j=parse_message(data,len);
    if (!j) { fail_round("bad_json","Invalid voice JSON"); return; }
    uint32_t v=0, epoch=0, turn=0;
    const char *type=string(j,"type");
    if (!type || !number(j,"v",&v) || v!=1 || !number(j,"epoch",&epoch)) { fail_round("bad_message","Missing voice protocol fields"); goto done; }
    if (epoch!=s_epoch) goto done; // delayed messages from old connections are inert
    if (!strcmp(type,"config")) {
        const cJSON *enabled=cJSON_GetObjectItemCaseSensitive(j,"enabled");
        uint32_t volume;
        if (!cJSON_IsBool(enabled) || !number(j,"volume",&volume) || volume>100) { fail_round("bad_config","Invalid voice configuration"); goto done; }
        s_volume=volume;
        if (!cJSON_IsTrue(enabled)) { capture_snapshot(false,s_epoch); abort_round(true); goto done; }
        if (!s_audio_ready) {
            esp_err_t err=audio_init();
            if (err!=ESP_OK) { ESP_LOGE("voice","audio init: %s",esp_err_to_name(err)); fail_round("audio_unavailable","Audio hardware initialization failed"); goto done; }
            portENTER_CRITICAL(&s_lock); s_audio_ready=true; portEXIT_CRITICAL(&s_lock);
        }
        capture_snapshot(true,s_epoch); goto done;
    }
    if (discard_round) goto done; // local cancellation fences already received rounds
    if (!number(j,"turn",&turn)) { fail_round("bad_turn","Missing voice turn"); goto done; }
    // A completed/cancelled turn must never reopen. New state/start messages may
    // advance the turn; end/cancel/text can only affect the current round.
    if (turn<s_turn) goto done;
    if (turn && turn<=s_closed_turn) {
        // Cancel is sent before cloud errors. Permit terminal presentation for
        // the current closed turn, but never reopen it or accept more audio.
        const char *state=string(j,"state");
        bool terminal=turn==s_turn && (!strcmp(type,"error") ||
            (!strcmp(type,"state") && state && (!strcmp(state,"idle") || !strcmp(state,"error"))));
        if (!terminal) goto done;
    }
    if (!strcmp(type,"state")) {
        const char *state=string(j,"state"), *text=string(j,"text");
        if (!state) { fail_round("bad_state","Missing voice state"); goto done; }
        bool valid=!strcmp(state,"idle") || !strcmp(state,"wake") || !strcmp(state,"listening") || !strcmp(state,"recognizing") || !strcmp(state,"thinking") || !strcmp(state,"synthesizing") || !strcmp(state,"speaking") || !strcmp(state,"error");
        if (!valid || (!turn && strcmp(state,"idle"))) { fail_round("bad_state","Invalid voice state"); goto done; }
        if (turn>s_turn) { abort_round(false); s_turn=turn; s_error_until=0; }
        if (!strcmp(state,"speaking")) goto done; // DMA completion owns speaking
        if (!strcmp(state,"idle")) { if (s_play.active) goto done; abort_round(true); }
        else if (!strcmp(state,"error")) { abort_round(false); ui(state,text); s_error_until=esp_timer_get_time()+5000000; }
        else {
            // Keep capture uninterrupted for a question spoken immediately
            // after wake. The avatar and listening waveform indicate readiness
            // without adding a local cue to the microphone input.
            ui(state,text);
        }
    } else if (!strcmp(type,"playback_start")) {
        uint32_t samples, volume;
        const cJSON *streaming_field=cJSON_GetObjectItemCaseSensitive(j,"streaming");
        bool streaming=cJSON_IsTrue(streaming_field);
        if (!turn || !s_audio_ready || (streaming_field && !cJSON_IsBool(streaming_field)) || !number(j,"samples",&samples) || !number(j,"volume",&volume) || volume>100 || (streaming?samples!=0:(!samples || samples>VOICE_MAX_SAMPLES))) { fail_round("bad_playback","Invalid playback declaration"); goto done; }
        // Duplicate starts must not reset sequence/credits on a live turn.
        if (s_play.active && turn==s_turn) { fail_round("duplicate_start","Playback already started"); goto done; }
        if (turn>s_turn) { abort_round(false); s_turn=turn; }
        bool started=streaming?voice_playback_stream_start(&s_play,s_epoch,turn):voice_playback_start(&s_play,s_epoch,turn,samples);
        if (!started || audio_play_start(s_epoch,turn)!=ESP_OK) { fail_round("audio_start","Playback could not start"); goto done; }
        s_volume=volume; playback_activity(); s_start_pending=true;
        // Initial credits are issued only after the asynchronous DMA flush.
    } else if (!strcmp(type,"playback_end")) {
        uint32_t samples=0;
        bool ended=turn==s_turn && (s_play.streaming
            ? (number(j,"samples",&samples) && voice_playback_stream_end(&s_play,s_epoch,turn,samples))
            : voice_playback_end(&s_play,s_epoch,turn));
        if (!ended) fail_round("sample_count","Playback end sample count mismatch");
    } else if (!strcmp(type,"cancel")) {
        if (turn==s_turn) abort_round(true);
    } else if (!strcmp(type,"transcript") || !strcmp(type,"response")) {
        const char *text=string(j,"text");
        if (turn==s_turn && text) voice_page_update(s_phase,text,s_turn);
    } else if (!strcmp(type,"error")) {
        if (turn==s_turn) { abort_round(false); ui("error",string(j,"message")); s_error_until=esp_timer_get_time()+5000000; }
    } else fail_round("unknown_type","Unknown voice message");
done:
    cJSON_Delete(j);
}
static void binary_message(const uint8_t *data, size_t len)
{
    voice_packet_t packet;
    if (!voice_packet_decode(data,len,&packet)) { fail_round("bad_binary","Invalid audio frame"); return; }
    if (packet.epoch!=s_epoch || packet.turn<s_turn || packet.turn<=s_closed_turn) return;
    if (s_start_pending || !voice_playback_receive(&s_play,&packet) || !audio_play_queue(&packet,s_volume)) { fail_round("audio_sequence","Audio sequence or credit violation"); return; }
    playback_activity();
}
static void rendered(void)
{
    audio_render_t render;
    if (audio_render_poll(&render)) {
        if (!render.samples) { fail_round("render_overflow","Playback completion queue overflow"); return; }
        if (!s_play.active || render.epoch!=s_epoch || render.turn!=s_turn) return;
        bool first=!s_play.started;
        if (!voice_playback_render(&s_play,render.epoch,render.turn,render.seq,render.samples)) { fail_round("render_sequence","Playback completion mismatch"); return; }
        if (first) { send_json("playing",s_turn,NULL); dashboard_voice_begin(); ui("speaking",NULL); }
        voice_page_level(render.level);
        send_json("credit",s_turn,",\"frames\":1");
        playback_activity();
    }
    if (voice_playback_done(&s_play)) {
        send_json("played",s_turn,NULL);
        abort_round(true);
    }
}
static void command(const event_t *event)
{
    if (event->kind==CMD_CANCEL || event->kind==CMD_CLEAR || (event->kind==CMD_MUTE && event->muted)) {
        voice_ws_discard_audio(s_epoch);
        if (event->order>s_discard_through) s_discard_through=event->order;
        // A local cancel may overtake a newer round announced before the tap.
        // Send/seal that round, otherwise the peer would ignore the old turn.
        // Every legal PCM round has an earlier state/start text declaration.
        for (size_t i=0;i<s_pending_count;i++) {
            const event_t *pending=&s_pending[i];
            if (pending->epoch!=s_epoch || pending->order>event->order) continue;
            cJSON *j=parse_message(pending->data,pending->len);
            uint32_t v, epoch, turn;
            const char *type=j?string(j,"type"):NULL;
            if (type && strcmp(type,"config") && number(j,"v",&v) && v==1 &&
                number(j,"epoch",&epoch) && epoch==s_epoch && number(j,"turn",&turn) && turn>s_turn) s_turn=turn;
            cJSON_Delete(j);
        }
    }
    switch (event->kind) {
    case CMD_WAKE: send_json("wake",0,NULL); break;
    case CMD_CANCEL: send_json("cancel",s_turn,NULL); abort_round(true); break;
    case CMD_CLEAR: send_json("clear",s_turn,NULL); abort_round(true); break;
    case CMD_MUTE:
        portENTER_CRITICAL(&s_lock); s_muted=event->muted; portEXIT_CRITICAL(&s_lock);
        voice_page_set_muted(event->muted);
        send_json("mute",s_turn,event->muted?",\"muted\":true":",\"muted\":false");
        if (event->muted) { send_json("cancel",s_turn,NULL); abort_round(true); }
        break;
    default: break;
    }
}
static void priority_controls(void)
{
    event_t event, local[CONTROL_DEPTH];
    unsigned count=0;
    // Snapshot local commands before scanning text. Every text at a command
    // barrier was atomically published before that command became visible.
    while (count<CONTROL_DEPTH && xQueueReceive(s_controls,&local[count],0)==pdTRUE) count++;
    for (unsigned i=0;i<CONTROL_DEPTH && xQueueReceive(s_text,&event,0)==pdTRUE;i++) {
        if (event.epoch!=s_epoch || !s_epoch || priority_cancel(&event)) free(event.data);
        else if (s_pending_count<CONTROL_DEPTH) s_pending[s_pending_count++]=event;
        else { free(event.data); voice_ws_close(event.epoch); }
    }
    for (unsigned i=0;i<count;i++) {
        event=local[i];
        uint32_t epoch=0; voice_ws_connected(&epoch);
        if (event.epoch==s_epoch && s_epoch && epoch==s_epoch) command(&event);
    }
}
static bool next_event(event_t *event)
{
    event_t binary, unseen;
    bool got=false;
    portENTER_CRITICAL(&s_lock);
    // A text/start arriving after the scan must not be overtaken by its PCM.
    // Producers use this same lock for sequence assignment + zero-wait send.
    if (xQueuePeek(s_text,&unseen,0)!=pdTRUE && xQueuePeek(s_controls,&unseen,0)!=pdTRUE) {
        bool has_binary=xQueuePeek(s_events,&binary,0)==pdTRUE;
        if (s_pending_count && (!has_binary || s_pending[0].order<binary.order)) {
            *event=s_pending[0];
            memmove(s_pending,s_pending+1,(--s_pending_count)*sizeof(*s_pending));
            got=true;
        } else got=has_binary && xQueueReceive(s_events,event,0)==pdTRUE;
    }
    portEXIT_CRITICAL(&s_lock);
    return got;
}
static void poll_start(void)
{
    if (!s_start_pending) return;
    esp_err_t err=audio_play_poll_start();
    if (err==ESP_ERR_NOT_FINISHED) return;
    s_start_pending=false;
    if (err!=ESP_OK) fail_round("audio_start","Playback could not start");
    else send_json("credit",s_turn,",\"frames\":32");
}
static void worker(void *arg)
{
    (void)arg;
    for (;;) {
        uint32_t epoch=0; voice_ws_connected(&epoch);
        if (epoch!=s_epoch) reset_connection(epoch);
        priority_controls();
        voice_ws_connected(&epoch);
        if (epoch!=s_epoch) continue; // queue overflow closed the session
        poll_start();
        rendered(); // one completion per pass, after cancellation
        int64_t now=esp_timer_get_time();
        if (s_error_until && now>=s_error_until) { s_error_until=0; ui("idle",""); }
        if (s_play.active && s_play_deadline && now>=s_play_deadline) fail_round("playback_timeout","Playback stalled before drain");
        event_t event;
        if (next_event(&event)) {
            if (event.epoch==s_epoch && s_epoch) {
                bool discard=event.order<=s_discard_through;
                if (event.kind==RX_TEXT) text_message(event.data,event.len,discard);
                else if (!discard) binary_message(event.data,event.len);
            }
            free(event.data);
        } else vTaskDelay(pdMS_TO_TICKS(5));
    }
}
static void capture(void *arg)
{
    (void)arg;
    uint32_t previous=0, previous_revision=UINT32_MAX, seq=0;
    int16_t pcm[VOICE_FRAME_SAMPLES]; uint8_t frame[VOICE_FRAME_BYTES];
    for (;;) {
        portENTER_CRITICAL(&s_lock);
        bool enabled=s_capture_enabled && s_audio_ready; uint32_t epoch=s_capture_epoch, revision=s_capture_revision;
        portEXIT_CRITICAL(&s_lock);
        if (!enabled || !epoch) { vTaskDelay(pdMS_TO_TICKS(20)); continue; }
        if (epoch!=previous || revision!=previous_revision) {
            audio_capture_reset();
            if (epoch!=previous) seq=0;
            previous=epoch; previous_revision=revision;
        }
        esp_err_t err=audio_capture(pcm);
        if (err!=ESP_OK) {
            ESP_LOGE("voice","capture/AEC failed: %s",esp_err_to_name(err));
            voice_ws_close(epoch); vTaskDelay(pdMS_TO_TICKS(20)); continue;
        }
        portENTER_CRITICAL(&s_lock); bool current=s_capture_enabled && s_capture_epoch==epoch && s_capture_revision==revision, muted=s_muted; portEXIT_CRITICAL(&s_lock);
        if (!current) continue;
        voice_packet_header(frame,1,epoch,0,seq++);
        for (size_t i=0;i<VOICE_FRAME_SAMPLES;i++) { uint16_t sample=muted?0:(uint16_t)pcm[i]; frame[20+2*i]=sample; frame[21+2*i]=sample>>8; }
        if (!muted) {
            uint32_t peak=0;
            for (size_t i=0;i<VOICE_FRAME_SAMPLES;i++) { int32_t value=pcm[i]; uint32_t magnitude=value<0?-value:value; if (magnitude>peak) peak=magnitude; }
            portENTER_CRITICAL(&s_lock); bool metering=s_mic_meter; portEXIT_CRITICAL(&s_lock);
            if (metering) voice_page_level(peak/32768.0f);
        }
        voice_ws_send(epoch,true,frame,sizeof(frame));
    }
}
bool voice_transport_receive(uint32_t epoch, bool binary, uint8_t *data, size_t len)
{
    event_t event={.epoch=epoch,.kind=binary?RX_BINARY:RX_TEXT,.data=data,.len=len};
    QueueHandle_t queue=binary?s_events:s_text;
    bool sent=false;
    if (queue && data && len && len<=4096) {
        portENTER_CRITICAL(&s_lock);
        event.order=++s_receive_order;
        sent=xQueueSend(queue,&event,0)==pdTRUE;
        portEXIT_CRITICAL(&s_lock);
    }
    if (!sent) { free(data); voice_ws_close(epoch); return false; }
    return true;
}
static bool request(event_kind_t kind, bool muted)
{
    uint32_t epoch;
    if (!s_controls || !voice_ws_connected(&epoch)) return false;
    event_t event={.epoch=epoch,.kind=kind,.muted=muted};
    portENTER_CRITICAL(&s_lock);
    event.order=s_receive_order;
    bool sent=xQueueSend(s_controls,&event,0)==pdTRUE;
    portEXIT_CRITICAL(&s_lock);
    if (sent) return true;
    voice_ws_close(epoch); return false; // never lose a cancellation silently
}
bool voice_request_wake(void) { return request(CMD_WAKE,false); }
bool voice_request_cancel(void) { return request(CMD_CANCEL,false); }
bool voice_request_clear(void) { return request(CMD_CLEAR,false); }
bool voice_request_mute(bool muted) { return request(CMD_MUTE,muted); }
static void delete_events(void)
{
    QueueHandle_t queues[]={s_events,s_text,s_controls};
    event_t event;
    for (size_t i=0;i<sizeof(queues)/sizeof(queues[0]);i++) if (queues[i]) {
        while (xQueueReceive(queues[i],&event,0)==pdTRUE) free(event.data);
        vQueueDelete(queues[i]);
    }
    for (size_t i=0;i<s_pending_count;i++) free(s_pending[i].data);
    s_pending_count=0; s_events=s_text=s_controls=NULL;
}
esp_err_t voice_init(void)
{
    if (s_events) return ESP_OK;
    s_events=xQueueCreate(32,sizeof(event_t));
    s_text=xQueueCreate(CONTROL_DEPTH,sizeof(event_t));
    s_controls=xQueueCreate(CONTROL_DEPTH,sizeof(event_t));
    if (!s_events || !s_text || !s_controls) { delete_events(); return ESP_ERR_NO_MEM; }
    TaskHandle_t worker_handle=NULL;
    if (xTaskCreate(worker,"voice",8192,NULL,5,&worker_handle)!=pdPASS) { delete_events(); return ESP_ERR_NO_MEM; }
    if (xTaskCreate(capture,"voice_mic",12288,NULL,5,NULL)!=pdPASS) { vTaskDelete(worker_handle); delete_events(); return ESP_ERR_NO_MEM; }
    return ESP_OK;
}
