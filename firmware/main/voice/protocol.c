#include "protocol.h"
#include <string.h>
static uint32_t le32(const uint8_t *p) { return (uint32_t)p[0] | (uint32_t)p[1]<<8 | (uint32_t)p[2]<<16 | (uint32_t)p[3]<<24; }
static void put32(uint8_t *p, uint32_t n) { for (int i=0;i<4;i++) p[i]=(uint8_t)(n>>(8*i)); }
bool voice_packet_decode(const uint8_t *d, size_t len, voice_packet_t *o)
{
    if (!d || !o || len<=VOICE_HEADER_SIZE || len>VOICE_FRAME_BYTES || (len&1) || memcmp(d,"AEAU",4) || d[4]!=1 || (d[5]!=1 && d[5]!=2) || d[6] || d[7]) return false;
    *o=(voice_packet_t){.kind=d[5],.epoch=le32(d+8),.turn=le32(d+12),.seq=le32(d+16),.pcm=d+20,.samples=(len-20)/2};
    return o->epoch!=0 && (o->kind!=1 || (o->turn==0 && o->samples==VOICE_FRAME_SAMPLES)) && (o->kind!=2 || o->turn!=0);
}
void voice_packet_header(uint8_t *o, uint8_t kind, uint32_t epoch, uint32_t turn, uint32_t seq)
{
    memcpy(o,"AEAU",4); o[4]=1; o[5]=kind; o[6]=o[7]=0; put32(o+8,epoch); put32(o+12,turn); put32(o+16,seq);
}
bool voice_playback_start(voice_playback_t *p, uint32_t epoch, uint32_t turn, uint32_t samples)
{
    if (!epoch || !turn || !samples || samples>VOICE_MAX_SAMPLES) return false;
    *p=(voice_playback_t){.epoch=epoch,.turn=turn,.declared=samples,.active=true}; return true;
}
bool voice_playback_receive(voice_playback_t *p, const voice_packet_t *f)
{
    if (!p->active || p->ended || f->kind!=2 || f->epoch!=p->epoch || f->turn!=p->turn || f->seq!=p->seq || !f->samples || f->samples>320 || p->outstanding>=32 || f->samples>p->declared-p->received || (f->samples<320 && p->received+f->samples!=p->declared)) return false;
    p->seq++; p->received+=(uint32_t)f->samples; p->outstanding++; return true;
}
bool voice_playback_end(voice_playback_t *p, uint32_t epoch, uint32_t turn)
{
    if (!p->active || p->ended || p->epoch!=epoch || p->turn!=turn || p->received!=p->declared) return false;
    p->ended=true; return true;
}
bool voice_playback_render(voice_playback_t *p, uint32_t epoch, uint32_t turn, uint32_t seq, size_t samples)
{
    if (!p->active || p->epoch!=epoch || p->turn!=turn || !p->outstanding || seq!=p->seq-p->outstanding || !samples || samples>p->received-p->rendered) return false;
    p->rendered+=(uint32_t)samples; p->outstanding--; p->started=true; return true;
}
bool voice_playback_done(const voice_playback_t *p) { return p->active && p->ended && !p->outstanding && p->rendered==p->declared; }
