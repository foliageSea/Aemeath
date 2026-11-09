//go:build darwin && cgo

package sessionlock

/*
#include <dlfcn.h>
#include <stdint.h>

typedef int32_t (*session_lock_fn)(void);
typedef struct { void *handle; session_lock_fn request; } session_lock_api;
static session_lock_api session_lock_open(void) {
 session_lock_api api = {0, 0};
 api.handle = dlopen("/System/Library/PrivateFrameworks/login.framework/Versions/A/login", RTLD_LAZY | RTLD_LOCAL);
 if (api.handle) {
  api.request = (session_lock_fn)dlsym(api.handle, "SACLockScreenImmediate");
 }
 return api;
}
static int32_t session_lock_call(session_lock_api api) { return api.request(); }
static void session_lock_close(session_lock_api api) { if(api.handle) dlclose(api.handle); }
*/
import "C"
import "context"

// The private system API is dynamically resolved, not linked as a framework.
// SACLockScreenImmediate returns OSStatus (zero means the request was accepted).
// Signature also recorded by Chromium base/mac/login_util.cc. Resolution is
// separate from invocation so cancellation is checked just before the effect.
func platformLock(ctx context.Context) error {
	if systemExecutable(cgSessionPath) {
		return lockCGSession(ctx, systemExecutable, runSystemCommand)
	}
	return lockNative(ctx, func() (func() int32, func(), bool) {
		api := C.session_lock_open()
		cleanup := func() { C.session_lock_close(api) }
		if api.request == nil {
			cleanup()
			return nil, nil, false
		}
		return func() int32 { return int32(C.session_lock_call(api)) }, cleanup, true
	})
}

func lockNative(ctx context.Context, resolve func() (request func() int32, cleanup func(), available bool)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	request, cleanup, available := resolve()
	if !available {
		return ErrUnavailable
	}
	defer cleanup()
	if err := ctx.Err(); err != nil {
		return err
	}
	return checkedStatus(request())
}
