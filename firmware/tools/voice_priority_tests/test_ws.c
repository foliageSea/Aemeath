#include "../../main/net/voice_ws.c"
bool voice_transport_receive(uint32_t epoch,bool binary,uint8_t *data,size_t len) { (void)epoch; (void)binary; (void)len; free(data); return true; }
static void setup(void) {
    delete_output(); assert(voice_ws_register((void *)1)==ESP_OK);
    s_epoch=17; s_fd=5; host_closes=host_sends=0;
}
static void test_output_priority(void) {
    setup();
    for(unsigned i=0;i<TX_DEPTH;i++) { uint8_t value=i; assert(voice_ws_send(17,true,&value,1)); }
    char cancel[]="cancel"; assert(voice_ws_send(17,false,cancel,sizeof(cancel)));
    memset(cancel,'x',sizeof(cancel)); // enqueue takes a copy, never the caller's stack
    output_t out; assert(next_output(&out));
    assert(out.type==HTTPD_WS_TYPE_TEXT && !memcmp(out.data,"cancel",7));
    for(unsigned i=0;i<TX_DEPTH;i++) { assert(next_output(&out)); assert(out.type==HTTPD_WS_TYPE_BINARY && out.data[0]==i); }
    assert(!next_output(&out));
    output_t pong={.epoch=17,.fd=5,.type=HTTPD_WS_TYPE_PONG};
    assert(voice_ws_send(17,true,"a",1)); assert(enqueue(&pong));
    assert(next_output(&out) && out.type==HTTPD_WS_TYPE_PONG);
}
static void test_fail_closed(void) {
    setup(); for(unsigned i=0;i<TX_CONTROL_DEPTH;i++) assert(voice_ws_send(17,false,"c",1));
    assert(!voice_ws_send(17,false,"c",1) && !s_epoch && host_closes==1);
    setup(); for(unsigned i=0;i<TX_DEPTH;i++) assert(voice_ws_send(17,true,"a",1));
    assert(!voice_ws_send(17,true,"a",1) && !s_epoch && host_closes==1);
}
static void test_epoch_and_ownership(void) {
    setup(); session_t session={.magic=SESSION_MAGIC,.epoch=17,.fd=5,.owner=true}; host_session=&session;
    assert(voice_ws_send(17,false,"cancel",7));
    output_t *out=malloc(sizeof(*out)); assert(next_output(out));
    send_work(out); assert(host_sends==1); // callback frees exactly once, checked by ASan
    assert(voice_ws_send(17,false,"old",4)); out=malloc(sizeof(*out)); assert(next_output(out));
    s_epoch=session.epoch=18; send_work(out); assert(host_sends==1 && !host_closes);
    assert(!voice_ws_send(17,false,"stale",6));
    // fd reuse must not make a new session inherit an old epoch's payload.
    s_epoch=17; assert(voice_ws_send(17,false,"old",4)); out=malloc(sizeof(*out)); assert(next_output(out));
    session.epoch=19; send_work(out); assert(host_sends==1 && !host_closes);
}
static void test_wake_and_cancel_capture(void) {
    setup();
    assert(voice_ws_send(17,true,"old-a",6));
    assert(voice_ws_send(17,true,"old-b",6));
    assert(voice_ws_send_ordered(17,"wake",5));
    assert(voice_ws_send(17,true,"new",4));
    assert(voice_ws_send(17,false,"credit",7));
    const char *expected[]={"credit","old-a","old-b","wake","new"};
    output_t out;
    for(unsigned i=0;i<5;i++) { assert(next_output(&out)); assert(!strcmp((char *)out.data,expected[i])); }
    setup(); session_t session={.magic=SESSION_MAGIC,.epoch=17,.fd=5,.owner=true}; host_session=&session;
    assert(voice_ws_send(17,true,"in-flight",10));
    assert(voice_ws_send(17,true,"old",4));
    assert(voice_ws_send_ordered(17,"superseded-wake",16));
    output_t *flight=malloc(sizeof(*flight)); assert(next_output(flight));
    voice_ws_discard_audio(18); assert(s_output->count==2); // stale reset cannot touch new owner
    voice_ws_discard_audio(17); assert(!s_output->count);
    assert(!strcmp((char *)flight->data,"in-flight"));
    send_work(flight); assert(host_sends==1); // reset never frees HTTPD-owned bytes
    assert(voice_ws_send(17,false,"cancel",7));
    assert(voice_ws_send_ordered(17,"next-wake",10));
    assert(voice_ws_send(17,true,"fresh",6));
    const char *after[]={"cancel","next-wake","fresh"};
    for(unsigned i=0;i<3;i++) { assert(next_output(&out)); assert(!strcmp((char *)out.data,after[i])); }
    assert(!next_output(&out));
}
int main(void) {
    test_wake_and_cancel_capture(); test_output_priority(); test_fail_closed(); test_epoch_and_ownership(); delete_output();
    puts("websocket priority: control/audio FIFO, payload ownership, stale epochs and overflow passed");
}
