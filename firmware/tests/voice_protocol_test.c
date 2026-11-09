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
int main(void){packet_validation();for(uint32_t i=1;i<=20;i++)playback_round(i);puts("voice protocol: validation, credits, epochs, sequences and20 drained turns PASS");}
