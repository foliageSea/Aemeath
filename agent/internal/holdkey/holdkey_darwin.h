#ifndef AEMEATH_HOLDKEY_DARWIN_H
#define AEMEATH_HOLDKEY_DARWIN_H
#include <stdint.h>
typedef struct HoldKeyListener HoldKeyListener;
int32_t holdkey_start(uintptr_t token, uint32_t keycode, uint32_t modifiers, HoldKeyListener **out);
void holdkey_stop(HoldKeyListener *listener);
extern void holdkey_changed(uintptr_t token, int down);
#endif
