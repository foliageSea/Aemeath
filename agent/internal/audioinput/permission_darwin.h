#ifndef AEMEATH_AUDIOINPUT_PERMISSION_H
#define AEMEATH_AUDIOINPUT_PERMISSION_H

typedef struct aemeath_mic_request aemeath_mic_request;
int aemeath_mic_permission_status(void);
int aemeath_mic_has_usage_description(void);
aemeath_mic_request *aemeath_mic_request_begin(void);
int aemeath_mic_request_result(aemeath_mic_request *request);
void aemeath_mic_request_release(aemeath_mic_request *request);

#endif
