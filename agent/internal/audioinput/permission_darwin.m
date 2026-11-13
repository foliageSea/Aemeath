//go:build cgo && darwin

#import <AVFoundation/AVFoundation.h>
#import <Foundation/Foundation.h>
#include <stdatomic.h>
#include <stdlib.h>
#include "permission_darwin.h"

struct aemeath_mic_request {
    atomic_int references;
    atomic_int result;
};

int aemeath_mic_permission_status(void) {
    @autoreleasepool {
        switch ([AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio]) {
            case AVAuthorizationStatusAuthorized: return 1;
            case AVAuthorizationStatusDenied: return -1;
            case AVAuthorizationStatusRestricted: return -2;
            default: return 0;
        }
    }
}

int aemeath_mic_has_usage_description(void) {
    @autoreleasepool {
        id value = [[NSBundle mainBundle] objectForInfoDictionaryKey:@"NSMicrophoneUsageDescription"];
        return [value isKindOfClass:[NSString class]] && [value length] > 0;
    }
}

void aemeath_mic_request_release(aemeath_mic_request *request) {
    if (atomic_fetch_sub(&request->references, 1) == 1) free(request);
}

aemeath_mic_request *aemeath_mic_request_begin(void) {
    aemeath_mic_request *request = calloc(1, sizeof(*request));
    if (request == NULL) return NULL;
    // One reference belongs to Go, the other to AVFoundation's completion block.
    atomic_init(&request->references, 2);
    atomic_init(&request->result, 0);
    @autoreleasepool {
        [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
            atomic_store(&request->result, granted ? 1 : -1);
            aemeath_mic_request_release(request);
        }];
    }
    return request;
}

int aemeath_mic_request_result(aemeath_mic_request *request) {
    return atomic_load(&request->result);
}
