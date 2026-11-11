#include <setjmp.h>
#include "../../main/voice/voice.c"
static jmp_buf host_capture_exit;
static bool host_capture_test;
static unsigned host_capture_reads, host_capture_resets;
static uint32_t host_epoch=17;
static unsigned host_aborts, host_credits, host_played, host_audio_frames, host_cancel_turn, host_purges, host_ordered;
static esp_err_t host_start_result=ESP_ERR_NOT_FINISHED;
static bool host_render_ready;
static audio_render_t host_render;
bool voice_ws_connected(uint32_t *epoch) { if(epoch) *epoch=host_epoch; return host_epoch!=0; }
void voice_ws_close(uint32_t epoch) { if(host_epoch==epoch) host_epoch=0; }
bool voice_ws_send(uint32_t epoch,bool binary,const void *data,size_t len) {
    assert(epoch==host_epoch);
    if(binary) {
        if(host_capture_test) {
            assert(len==VOICE_FRAME_BYTES);
            for(size_t i=VOICE_HEADER_SIZE;i<len;i++) assert(((const uint8_t *)data)[i]==0);
            longjmp(host_capture_exit,1);
        }
        return true;
    }
    if(strstr(data,"\"type\":\"credit\"")) host_credits++;
    if(strstr(data,"\"type\":\"played\"")) host_played++;
    if(strstr(data,"\"type\":\"cancel\"")) { cJSON *j=cJSON_Parse(data); assert(number(j,"turn",&host_cancel_turn)); cJSON_Delete(j); }
    return true;
}
bool voice_ws_send_ordered(uint32_t epoch,const void *data,size_t len) { host_ordered++; return voice_ws_send(epoch,false,data,len); }
void voice_ws_discard_audio(uint32_t epoch) { assert(epoch==host_epoch); host_purges++; }
void audio_capture_reset(void) { host_capture_resets++; }
esp_err_t audio_init(void) { return ESP_OK; }
esp_err_t audio_capture(int16_t pcm[VOICE_FRAME_SAMPLES]) {
    if(host_capture_test) {
        host_capture_reads++;
        for(size_t i=0;i<VOICE_FRAME_SAMPLES;i++) pcm[i]=1234;
        if(host_capture_reads==1) { capture_snapshot(false,17); capture_snapshot(true,17); }
        else { assert(host_capture_reads==2); s_muted=true; }
    }
    return ESP_OK;
}
esp_err_t audio_play_start(uint32_t epoch,uint32_t turn) { assert(epoch && turn); return ESP_OK; }
esp_err_t audio_play_poll_start(void) { return host_start_result; }
void audio_play_abort(void) { host_aborts++; host_render_ready=false; }
bool audio_play_queue(const voice_packet_t *packet,int volume) { (void)packet; (void)volume; host_audio_frames++; return true; }
bool audio_render_poll(audio_render_t *render) { if(!host_render_ready) return false; *render=host_render; host_render_ready=false; return true; }
static void setup(void) {
    delete_events();
    assert(voice_init()==ESP_OK);
    host_epoch=s_epoch=17; s_turn=5; s_closed_turn=4; s_receive_order=s_discard_through=0;
    s_audio_ready=true; s_muted=s_start_pending=false;
    memset(&s_play,0,sizeof(s_play));
    host_aborts=host_credits=host_played=host_audio_frames=host_cancel_turn=host_purges=host_ordered=0;
    host_render_ready=false; host_start_result=ESP_ERR_NOT_FINISHED;
}
static void text(const char *json) {
    size_t len=strlen(json); uint8_t *data=malloc(len+1); memcpy(data,json,len+1);
    assert(voice_transport_receive(17,false,data,len));
}
static void start(unsigned turn) {
    char json[200]; snprintf(json,sizeof(json),"{\"v\":1,\"type\":\"playback_start\",\"epoch\":17,\"turn\":%u,\"samples\":320,\"volume\":70}",turn); text(json);
}
static void pcm(unsigned turn) {
    uint8_t *data=calloc(1,VOICE_FRAME_BYTES+1); voice_packet_header(data,2,17,turn,0);
    assert(voice_transport_receive(17,true,data,VOICE_FRAME_BYTES));
}
static void consume(void) {
    event_t event; assert(next_event(&event));
    if(event.epoch==s_epoch) {
        bool discard=event.order<=s_discard_through;
        if(event.kind==RX_TEXT) text_message(event.data,event.len,discard);
        else if(!discard) binary_message(event.data,event.len);
    }
    free(event.data);
}
static void test_remote_cancel(void) {
    setup(); assert(voice_playback_start(&s_play,17,5,320));
    for(unsigned i=0;i<32;i++) pcm(5);
    start(6);
    text("{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":6}");
    host_render_ready=true; host_render=(audio_render_t){.epoch=17,.turn=5,.samples=320};
    priority_controls();
    assert(s_turn==6 && s_closed_turn==6 && !s_play.active && host_aborts==1);
    rendered(); assert(!host_credits && !host_played);
    for(unsigned i=0;i<33;i++) consume();
    assert(!s_play.active && !host_audio_frames);
    start(7); priority_controls(); consume();
    assert(s_turn==7 && s_start_pending && !host_credits);
    poll_start(); assert(s_start_pending && !host_credits);
    host_start_result=ESP_OK; poll_start(); assert(!s_start_pending && host_credits==1);
}
static void test_audio_order(void) {
    setup(); start(6); pcm(6);
    text("{\"v\":1,\"type\":\"playback_end\",\"epoch\":17,\"turn\":6}");
    priority_controls(); assert(s_pending_count==2);
    consume(); assert(s_play.active && s_start_pending);
    host_start_result=ESP_OK; poll_start();
    consume(); assert(s_play.received==320 && host_audio_frames==1);
    consume(); assert(s_play.ended && !host_played);
    host_render=(audio_render_t){.epoch=17,.turn=6,.seq=0,.samples=320}; host_render_ready=true;
    rendered(); assert(host_played==1 && s_closed_turn==6);
}
static void test_validation(void) {
    const char *invalid[]={
        "{\"v\":2,\"type\":\"cancel\",\"epoch\":17,\"turn\":5}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":5.5}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":-1}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":4294967296}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":\"5\"}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":5,\"turn\":6}",
        "{\"v\":1,\"type\":\"cancel\\u0000suffix\",\"epoch\":17,\"turn\":5}",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":5} trailing",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":5} { }",
        "{\"v\":1,\"type\":\"cancel\",\"epoch\":18,\"turn\":5}"
    };
    setup();
    for(size_t i=0;i<sizeof(invalid)/sizeof(invalid[0]);i++) {
        event_t event={.epoch=17,.order=1,.data=(uint8_t *)invalid[i],.len=strlen(invalid[i])};
        assert(!priority_cancel(&event)); assert(!host_aborts && s_turn==5 && s_closed_turn==4);
    }
    const char *stale[]={"{\"v\":1,\"type\":\"cancel\",\"epoch\":17,\"turn\":4}",
                         "{\"v\":1,\"type\":\"cancel\",\"epoch\":18,\"turn\":5}"};
    for(unsigned i=0;i<2;i++) {
        event_t event={.epoch=i?18:17,.order=1,.data=(uint8_t *)stale[i],.len=strlen(stale[i])};
        assert(priority_cancel(&event)); assert(!host_aborts);
    }
}
static void test_local_barrier(void) {
    setup(); assert(voice_request_wake()); priority_controls();
    assert(host_ordered==1 && !host_purges);
    assert(voice_request_mute(false)); priority_controls(); assert(!host_purges);
    setup(); start(6); assert(voice_request_cancel()); start(7);
    priority_controls(); assert(s_closed_turn==6 && s_discard_through==1 && host_cancel_turn==6 && host_purges==1);
    consume(); assert(s_turn==6 && !s_play.active);
    text("{\"v\":1,\"type\":\"state\",\"epoch\":17,\"turn\":6,\"state\":\"synthesizing\"}");
    priority_controls();
    consume(); assert(s_turn==7 && s_play.active && s_start_pending);
    consume(); assert(s_turn==7 && s_play.active); // delayed canceled turn is inert
    assert(voice_request_mute(true)); priority_controls(); poll_start();
    assert(s_muted && !s_start_pending && !host_credits && s_closed_turn==7 && host_purges==2);
    setup(); start(6); assert(voice_request_clear()); priority_controls(); consume();
    assert(!s_play.active && s_closed_turn==6);
    setup(); assert(voice_request_cancel()); s_epoch=host_epoch=18;
    priority_controls(); assert(!host_aborts); // old epoch local commands are inert
}
static void test_overflow(void) {
    setup(); for(unsigned i=0;i<32;i++) pcm(5);
    uint8_t *extra=calloc(1,VOICE_FRAME_BYTES+1);
    assert(!voice_transport_receive(17,true,extra,VOICE_FRAME_BYTES) && !host_epoch);
    setup(); for(unsigned i=0;i<CONTROL_DEPTH;i++) assert(voice_request_cancel());
    assert(!voice_request_clear() && !host_epoch);
    setup(); for(unsigned i=0;i<CONTROL_DEPTH;i++) start(6);
    const char *json="{}"; extra=(uint8_t *)strdup(json);
    assert(!voice_transport_receive(17,false,extra,strlen(json)) && !host_epoch);
    setup(); for(unsigned i=0;i<CONTROL_DEPTH;i++) start(6);
    priority_controls(); assert(s_pending_count==CONTROL_DEPTH);
    start(7); priority_controls(); assert(!host_epoch); // bounded worker staging also fails closed
}
static void test_scan_boundary(void) {
    setup(); priority_controls(); // empty scan, then HTTPD delivers start + PCM
    start(6); pcm(6);
    event_t event; assert(!next_event(&event));
    priority_controls(); consume(); assert(s_start_pending);
    host_start_result=ESP_OK; poll_start(); consume();
    assert(s_play.received==320);
    setup(); start(6); priority_controls(); consume();
    host_start_result=ESP_ERR_TIMEOUT; poll_start();
    assert(!s_start_pending && !s_play.active && s_closed_turn==6);
}
static void test_capture_revision(void) {
    setup(); capture_snapshot(true,17);
    host_capture_reads=host_capture_resets=0; host_capture_test=true;
    if(!setjmp(host_capture_exit)) capture(NULL);
    host_capture_test=false;
    assert(host_capture_reads==2 && host_capture_resets==2);
}
int main(void) {
    test_capture_revision(); test_scan_boundary(); test_remote_cancel(); test_audio_order(); test_validation(); test_local_barrier(); test_overflow();
    delete_events(); puts("voice priority: order, validation, stale epochs, barriers, async start and overflow passed");
}
