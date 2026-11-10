//go:build darwin && cgo

#include "holdkey_darwin.h"
#include <Carbon/Carbon.h>
#include <stdlib.h>

struct HoldKeyListener {
    EventHotKeyRef hotkey;
    EventHandlerRef handler;
    uintptr_t token;
};

static OSStatus holdkey_event(EventHandlerCallRef next, EventRef event, void *data) {
    (void)next;
    HoldKeyListener *listener = (HoldKeyListener *)data;
    EventHotKeyID id;
    OSStatus status = GetEventParameter(event, kEventParamDirectObject,
        typeEventHotKeyID, NULL, sizeof(id), NULL, &id);
    if (status != noErr || id.signature != 'AeF8' || id.id != (UInt32)listener->token)
        return eventNotHandledErr;
    UInt32 kind = GetEventKind(event);
    if (kind != kEventHotKeyPressed && kind != kEventHotKeyReleased)
        return eventNotHandledErr;
    holdkey_changed(listener->token, kind == kEventHotKeyPressed);
    return noErr;
}

int32_t holdkey_start(uintptr_t token, int modified, HoldKeyListener **out) {
    *out = NULL;
    HoldKeyListener *listener = calloc(1, sizeof(*listener));
    if (!listener) return memFullErr;
    listener->token = token;
    const EventTypeSpec types[] = {
        { kEventClassKeyboard, kEventHotKeyPressed },
        { kEventClassKeyboard, kEventHotKeyReleased }
    };
    OSStatus status = InstallEventHandler(GetApplicationEventTarget(),
        holdkey_event, 2, types, listener, &listener->handler);
    if (status != noErr) { free(listener); return status; }
    EventHotKeyID id = { 'AeF8', (UInt32)token };
    status = RegisterEventHotKey(100, modified ? (controlKey | shiftKey) : 0, id, GetApplicationEventTarget(),
        kEventHotKeyExclusive, &listener->hotkey);
    if (status != noErr) {
        RemoveEventHandler(listener->handler);
        free(listener);
        return status;
    }
    *out = listener;
    return noErr;
}

void holdkey_stop(HoldKeyListener *listener) {
    UnregisterEventHotKey(listener->hotkey);
    RemoveEventHandler(listener->handler);
    free(listener);
}
