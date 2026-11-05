// Minimal captive DNS: replies to every A query with the SoftAP IP.
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lwip/sockets.h"
#include "net.h"

static const char *TAG = "dns";

static TaskHandle_t s_task;
static volatile bool s_run;

static void dns_task(void *arg)
{
    int sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    struct sockaddr_in addr = {
        .sin_family = AF_INET,
        .sin_port = htons(53),
        .sin_addr.s_addr = htonl(INADDR_ANY),
    };
    if (sock < 0 || bind(sock, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        ESP_LOGE(TAG, "bind failed");
        goto out;
    }
    struct timeval tv = {.tv_sec = 1};
    setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));

    uint8_t buf[512];
    const uint32_t ap_ip = inet_addr(NET_AP_IP);
    while (s_run) {
        struct sockaddr_in from;
        socklen_t flen = sizeof(from);
        int n = recvfrom(sock, buf, sizeof(buf) - 16, 0, (struct sockaddr *)&from, &flen);
        if (n < 12) {
            continue;
        }
        // Walk the first question's name to find its end.
        int p = 12;
        while (p < n && buf[p]) {
            p += buf[p] + 1;
        }
        p += 5;  // zero byte + QTYPE + QCLASS
        if (p > n) {
            continue;
        }
        uint16_t qtype = (buf[p - 4] << 8) | buf[p - 3];
        buf[2] = 0x81;  // response, recursion desired
        buf[3] = 0x80;  // recursion available, no error
        buf[6] = 0; buf[7] = (qtype == 1) ? 1 : 0;   // ANCOUNT
        buf[8] = buf[9] = buf[10] = buf[11] = 0;     // NSCOUNT, ARCOUNT
        int len = p;
        if (qtype == 1) {
            const uint8_t ans[] = {0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4};
            memcpy(buf + len, ans, sizeof(ans));
            len += sizeof(ans);
            memcpy(buf + len, &ap_ip, 4);
            len += 4;
        }
        sendto(sock, buf, len, 0, (struct sockaddr *)&from, flen);
    }
out:
    if (sock >= 0) {
        close(sock);
    }
    s_task = NULL;
    vTaskDelete(NULL);
}

void dns_hijack_start(void)
{
    if (s_task) {
        return;
    }
    s_run = true;
    xTaskCreate(dns_task, "dns", 3072, NULL, 4, &s_task);
}

void dns_hijack_stop(void)
{
    s_run = false;
}
