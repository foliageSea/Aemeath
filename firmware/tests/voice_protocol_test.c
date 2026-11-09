#include "protocol.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>

static void packet_validation(void) {
 uint8_t raw[VOICE_FRAME_BYTES]={0}; voice_packet_t p;
 voice_packet_header(raw,1,42,0,0);
 assert(voice_packet_decode(raw,sizeof(raw),&p));
 assert(p.epoch==42&&p.turn==0&&p.samples==320);
 assert(!voice_packet_decode(raw,19,&p));
 assert(!voice_packet_decode(raw,sizeof(raw)-1,&p));
 raw[6]=1;assert(!voice_packet_decode(raw,sizeof(raw),&p));raw[6]=0;
 raw[4]=2;assert(!voice_packet_decode(raw,sizeof(raw),&p));
 voice_packet_header(raw,1,42,7,0);assert(!voice_packet_decode(raw,sizeof(raw),&p));
 voice_packet_header(raw,2,42,0,0);assert(!voice_packet_decode(raw,22,&p));
 voice_packet_header(raw,2,42,1,0);assert(voice_packet_decode(raw,22,&p));
}
static void playback_round(uint32_t turn) {
 voice_playback_t state;
 const uint32_t total=320*40+7;
 assert(voice_playback_start(&state,42,turn,total));
 uint8_t bytes[VOICE_PCM_BYTES]={0};
 voice_packet_t p={.epoch=42,.turn=turn,.kind=2,.pcm=bytes,.samples=320};
 for(uint32_t i=0;i<32;i++){p.seq=i;assert(voice_playback_receive(&state,&p));}
 p.seq=32;assert(!voice_playback_receive(&state,&p)); // no credits
 assert(!voice_playback_end(&state,42,turn)); // insufficient samples
 assert(!voice_playback_render(&state,41,turn,0,320));
 assert(!voice_playback_render(&state,42,turn,1,320));
 for(uint32_t i=0;i<32;i++)assert(voice_playback_render(&state,42,turn,i,320));
 for(uint32_t i=32;i<40;i++){p.seq=i;assert(voice_playback_receive(&state,&p));}
 p.seq=40;p.samples=7;assert(voice_playback_receive(&state,&p));
 assert(voice_playback_end(&state,42,turn));
 assert(!voice_playback_end(&state,42,turn));
 assert(!voice_playback_receive(&state,&p));
 assert(!voice_playback_done(&state));
 for(uint32_t i=32;i<40;i++)assert(voice_playback_render(&state,42,turn,i,320));
 assert(!voice_playback_done(&state));
 assert(voice_playback_render(&state,42,turn,40,7));
 assert(voice_playback_done(&state));
 assert(!voice_playback_render(&state,42,turn,40,7));
}
static voice_packet_t stream_frame(uint32_t seq, size_t samples) {
 return (voice_packet_t){.epoch=42,.turn=7,.kind=2,.seq=seq,.samples=samples};
}
static void streaming_mode_validation(void) {
 voice_playback_t state={0};
 assert(!voice_playback_start(&state,42,7,0)); // v1 fixed-zero stays invalid
 assert(!voice_playback_start(&state,42,7,VOICE_MAX_SAMPLES+1));
 assert(!voice_playback_stream_start(&state,0,7));
 assert(!voice_playback_stream_start(&state,42,0));
 assert(voice_playback_stream_start(&state,42,7));
 assert(state.streaming && state.active && !state.ended && !state.declared);
 assert(voice_playback_idle_ms(&state)==30000);
 assert(!voice_playback_done(&state)); // not even an empty stream may self-finish
 assert(!voice_playback_end(&state,42,7)); // old end cannot close a stream
 assert(!voice_playback_stream_end(&state,42,7,0));
 assert(!voice_playback_stream_end(&state,42,7,1));
 assert(!voice_playback_stream_end(&state,42,7,VOICE_MAX_SAMPLES+1));
 assert(voice_playback_start(&state,42,7,320));
 assert(!state.streaming && !state.final_short);
 assert(voice_playback_idle_ms(&state)==5000);
 assert(!voice_playback_stream_end(&state,42,7,320));
}
static void streaming_underflow_and_late_end(void) {
 voice_playback_t state;
 assert(voice_playback_stream_start(&state,42,7));
 // Repeated complete starvation of the queue must leave the same stream open.
 for(uint32_t seq=0;seq<5;seq++) {
  voice_packet_t p=stream_frame(seq,320);
  assert(voice_playback_receive(&state,&p));
  assert(voice_playback_render(&state,42,7,seq,320));
  assert(!state.outstanding && state.active && state.started && !state.ended);
  assert(!voice_playback_done(&state));
  assert(voice_playback_idle_ms(&state)==30000);
 }
 assert(!voice_playback_stream_end(&state,41,7,1600));
 assert(!voice_playback_stream_end(&state,42,6,1600));
 assert(!voice_playback_stream_end(&state,42,7,1599));
 assert(!state.declared && !state.ended);
 assert(voice_playback_stream_end(&state,42,7,1600));
 assert(voice_playback_done(&state)); // valid end may arrive after the final DMA ACK
 assert(!voice_playback_stream_end(&state,42,7,1600));
 voice_packet_t p=stream_frame(5,320);
 assert(!voice_playback_receive(&state,&p));
}
static void streaming_end_before_drain(void) {
 voice_playback_t state;
 assert(voice_playback_stream_start(&state,42,7));
 for(uint32_t seq=0;seq<3;seq++) {
  voice_packet_t p=stream_frame(seq,seq==2?7:320);
  assert(voice_playback_receive(&state,&p));
 }
 assert(state.final_short);
 assert(!voice_playback_stream_end(&state,42,7,648));
 assert(voice_playback_stream_end(&state,42,7,647));
 assert(!voice_playback_done(&state));
 assert(!voice_playback_render(&state,42,7,0,319));
 assert(!voice_playback_render(&state,42,7,1,320));
 assert(voice_playback_render(&state,42,7,0,320));
 assert(voice_playback_render(&state,42,7,1,320));
 assert(!voice_playback_done(&state));
 assert(!voice_playback_render(&state,42,7,2,6));
 assert(!voice_playback_render(&state,42,7,2,8));
 assert(voice_playback_render(&state,42,7,2,7));
 assert(voice_playback_done(&state));
}
static void streaming_short_final_only(void) {
 for(size_t samples=1;samples<VOICE_FRAME_SAMPLES;samples++) {
  voice_playback_t state;
  assert(voice_playback_stream_start(&state,42,7));
  voice_packet_t p=stream_frame(0,samples);
  assert(voice_playback_receive(&state,&p));
  assert(!voice_playback_done(&state));
  p.seq=1; p.samples=320;
  assert(!voice_playback_receive(&state,&p));
  p.samples=1; assert(!voice_playback_receive(&state,&p));
  assert(voice_playback_render(&state,42,7,0,samples));
  assert(!voice_playback_done(&state)); // short frame alone is not an end marker
  assert(voice_playback_stream_end(&state,42,7,(uint32_t)samples));
  assert(voice_playback_done(&state));
 }
 // Fixed mode still rejects a short frame that is not its declared final frame.
 voice_playback_t state;
 assert(voice_playback_start(&state,42,7,640));
 voice_packet_t p=stream_frame(0,7);
 assert(!voice_playback_receive(&state,&p));
}
static void streaming_credits_and_stale_frames(void) {
 voice_playback_t state;
 assert(voice_playback_stream_start(&state,42,7));
 for(uint32_t seq=0;seq<32;seq++) {
  voice_packet_t p=stream_frame(seq,320);
  assert(voice_playback_receive(&state,&p));
 }
 voice_packet_t p=stream_frame(32,320);
 assert(!voice_playback_receive(&state,&p));
 assert(state.outstanding==32 && state.received==10240 && state.seq==32);
 assert(!voice_playback_render(&state,41,7,0,320));
 assert(!voice_playback_render(&state,42,6,0,320));
 assert(voice_playback_render(&state,42,7,0,320));
 assert(voice_playback_receive(&state,&p)); // one rendered frame returns one credit
 for(uint32_t seq=1;seq<=32;seq++) assert(voice_playback_render(&state,42,7,seq,320));
 p=stream_frame(33,320); p.epoch=41; assert(!voice_playback_receive(&state,&p));
 p.epoch=42; p.turn=6; assert(!voice_playback_receive(&state,&p));
 p.turn=7; p.seq=34; assert(!voice_playback_receive(&state,&p));
 p.seq=33; p.kind=1; assert(!voice_playback_receive(&state,&p));
 assert(!state.outstanding && !state.ended && !voice_playback_done(&state));
 assert(voice_playback_stream_end(&state,42,7,10560));
 assert(voice_playback_done(&state));
 // Worker cancel/disconnect clears this state before another stream can start.
 memset(&state,0,sizeof(state)); p=stream_frame(0,320);
 assert(!voice_playback_receive(&state,&p));
 assert(!voice_playback_render(&state,42,7,33,320));
 assert(!voice_playback_stream_end(&state,42,7,10560));
 assert(voice_playback_stream_start(&state,43,8));
 assert(!voice_playback_receive(&state,&p)); // late old-epoch data
 assert(!voice_playback_stream_end(&state,42,7,320));
 p.epoch=43; p.turn=8;
 assert(voice_playback_receive(&state,&p));
 assert(voice_playback_stream_end(&state,43,8,320));
 assert(!voice_playback_render(&state,42,7,0,320));
 assert(!voice_playback_done(&state));
 assert(voice_playback_render(&state,43,8,0,320));
 assert(voice_playback_done(&state));
}
static void streaming_sample_limit(void) {
 voice_playback_t state;
 assert(voice_playback_stream_start(&state,42,7));
 for(uint32_t seq=0;seq<VOICE_MAX_SAMPLES/VOICE_FRAME_SAMPLES;seq++) {
  voice_packet_t p=stream_frame(seq,320);
  assert(voice_playback_receive(&state,&p));
  assert(voice_playback_render(&state,42,7,seq,320));
 }
 assert(state.received==VOICE_MAX_SAMPLES && state.rendered==VOICE_MAX_SAMPLES);
 assert(!voice_playback_done(&state));
 voice_packet_t p=stream_frame(state.seq,1);
 assert(!voice_playback_receive(&state,&p)); // even one extra sample exceeds 120s
 assert(!voice_playback_stream_end(&state,42,7,VOICE_MAX_SAMPLES+1));
 assert(voice_playback_stream_end(&state,42,7,VOICE_MAX_SAMPLES));
 assert(voice_playback_done(&state));
 // Zero-length and oversized frames are rejected before the total is known.
 assert(voice_playback_stream_start(&state,42,7));
 p=stream_frame(0,0); assert(!voice_playback_receive(&state,&p));
 p.samples=321; assert(!voice_playback_receive(&state,&p));
}
int main(void) {
 packet_validation();
 for(uint32_t i=1;i<=20;i++) playback_round(i);
 streaming_mode_validation();
 streaming_underflow_and_late_end();
 streaming_end_before_drain();
 streaming_short_final_only();
 streaming_credits_and_stale_frames();
 streaming_sample_limit();
 puts("voice protocol: fixed/streaming, underflow, end totals, final short, credits, epochs, cancellation and rendered drain PASS");
}
