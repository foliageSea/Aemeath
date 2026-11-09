#pragma once
#include <stdbool.h>
bool voice_request_wake(void);
bool voice_request_cancel(void);
bool voice_request_clear(void);
bool voice_request_mute(bool muted);
