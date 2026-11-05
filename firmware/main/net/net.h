// Wi-Fi STA with SoftAP captive-portal fallback, mDNS, HTTP/WebSocket server.
#pragma once

#include <stdbool.h>

typedef enum {
    NET_CONNECTING,
    NET_CONNECTED,
    NET_PORTAL,     // SoftAP up, waiting for credentials (STA may still retry)
} net_state_t;

typedef struct {
    net_state_t state;
    char sta_ssid[33];
    char ip[16];
    char ap_ssid[33];
    int rssi;
} net_status_t;

#define NET_MDNS_HOST     "aemeath"
#define NET_MDNS_SERVICE  "_aemeath"
#define NET_AP_IP         "192.168.4.1"

void net_start(void);
void net_get_status(net_status_t *out);

// Stores credentials and reboots.
void net_save_credentials(const char *ssid, const char *pass);
// Erases credentials and reboots into the portal.
void net_forget_credentials(void);

// server.c
void server_start(void);
// dns.c: answers every A query with the SoftAP address.
void dns_hijack_start(void);
void dns_hijack_stop(void);
