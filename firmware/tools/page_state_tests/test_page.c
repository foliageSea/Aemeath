#include "host.h"
#include "net/page_state.c"

bool voice_ws_is_client(int fd) { return fd == 20; }
static void flush(void)
{
    assert(pending);
    void (*fn)(void *) = pending;
    void *arg = pending_arg;
    pending = NULL;
    fn(arg);
}
static void expect_page(const char *page)
{
    char expected[64];
    snprintf(expected, sizeof(expected), "{\"v\":1,\"type\":\"page_state\",\"page\":\"%s\"}", page);
    assert(!strcmp(last_message, expected));
}
static void *producer(void *arg)
{
    (void)arg;
    for (unsigned i = 0; i < 10000; ++i) server_publish_page(i % 3);
    server_publish_page(2);
    return NULL;
}
int main(void)
{
    server_page_state_start((void *)1);
    page_state_queue(); // connecting before UI initialization must not invent metrics
    assert(!pending && !sends && !queued);
    server_publish_page(-1); server_publish_page(99);
    page_state_queue(); assert(!pending);
    lvgl_locked = true;
    server_publish_page(1); // actual quota before first agent connects
    unsigned before = notifications;
    server_publish_page(1); // repeated timer/event publication is deduplicated
    assert(notifications == before && queued == 0);
    server_broadcast_page(); // handshake only wakes a task, even if on HTTPD
    assert(notifications == before + 1 && queued == 0);
    lvgl_locked = false;
    page_state_queue(); flush();
    assert(sends == 2); expect_page("quota");
    for (int page = 0; page < 3; ++page) {
        lvgl_locked = true; server_publish_page(page); lvgl_locked = false;
        page_state_queue(); flush();
        expect_page(page == 0 ? "metrics" : page == 1 ? "quota" : "voice");
    }
    before = sends;
    server_broadcast_page(); // reconnect while page unchanged still reports real voice
    page_state_queue(); flush(); assert(sends == before + 2); expect_page("voice");
    server_publish_page(0); page_state_queue();
    before = queued;
    lvgl_locked = true; // simulate HTTPD blocked applying a command under the LVGL lock
    pthread_t thread;
    assert(!pthread_create(&thread, NULL, producer, NULL));
    assert(!pthread_join(thread, NULL)); // publication cannot wait for queued HTTP work
    lvgl_locked = false;
    page_state_queue(); assert(queued == before); // one bounded pending callback
    flush(); expect_page("voice"); // snapshot at execution, not stale queued metrics
    server_publish_page(1); page_state_queue(); flush(); expect_page("quota");
    queue_fail = true;
    server_publish_page(0); page_state_queue();
    assert(!pending && !s_work_pending && retries == 1);
    queue_fail = false;
    page_state_queue(); flush(); expect_page("metrics");
    server_publish_page(1); page_state_queue();
    server_publish_page(-1); // an invalidated active tile cannot leak the last known page
    before = sends; flush(); assert(sends == before);
    server_broadcast_page(); page_state_queue(); assert(!pending);
    server_publish_page(2); page_state_queue(); flush(); expect_page("voice");
    puts("PASS: page_state unknown/connect/reconnect, all pages, dedup, latest snapshot, bounded async queue, retry, voice exclusion");
}
