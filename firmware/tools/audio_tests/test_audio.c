#include "host.h"
/* Include the production unit so tests drive its registered hardware callbacks
 * and can inject otherwise unobservable ISR sequence/queue faults. */
#include "../../main/audio/audio.c"

static int16_t dma[4][640];
static size_t schedule_tick;
static int64_t schedule_interval;
static bool schedule_no_tx, schedule_extra_tx;

static void send_dma(unsigned index)
{
    i2s_event_data_t event = {.dma_buf = dma[index], .size = sizeof(dma[index])};
    assert(host_tx.enabled && host_tx.callbacks.on_sent);
    host_tx.callbacks.on_sent(&host_tx, &event, host_tx.ctx);
}

static void send_next(void)
{
    host_now += 20000;
    send_dma(s_cycles % 4u);
}

static void fixture(size_t native_size)
{
    host_wait_hook = NULL;
    host_aec_create_hook = NULL;
    host_pa_off_error = host_pa_on_error = 0;
    if (s_ready) cleanup();
    assert(host_aec_creates == host_aec_destroys);
    host_now = 1000000;
    host_pa = false; host_pa_on_calls = host_pa_off_calls = 0;
    host_waits = host_delays = 0;
    host_aec_creates = host_aec_destroys = 0;
    host_aec_count = host_aec_calls = 0;
    host_aec_fail_create = false; host_native_size = native_size;
    schedule_tick = 0; schedule_interval = 20000;
    schedule_no_tx = schedule_extra_tx = false;
    memset(dma, 0, sizeof(dma));
    assert(audio_init() == ESP_OK);
    assert(host_aec_creates == 1 && host_aec_destroys == 0);
    assert(!host_pa);
}

static void start_ready(void)
{
    unsigned waits = host_waits, delays = host_delays;
    assert(audio_play_start(42, 7) == ESP_OK);
    assert(audio_play_poll_start() == ESP_ERR_NOT_FINISHED);
    for (unsigned i = 0; i < 4; ++i) send_next();
    assert(!host_pa);
    assert(audio_play_poll_start() == ESP_OK);
    assert(host_pa);
    assert(waits == host_waits && delays == host_delays);
}

static voice_packet_t packet(uint8_t *pcm, size_t count, uint32_t sequence)
{
    return (voice_packet_t){.epoch = 42, .turn = 7, .seq = sequence,
                            .kind = 2, .pcm = pcm, .samples = count};
}

static void pcm_write(uint8_t *pcm, size_t index, int16_t value)
{
    pcm[2 * index] = (uint8_t)(uint16_t)value;
    pcm[2 * index + 1] = (uint8_t)((uint16_t)value >> 8);
}

static void test_played_reference_volume_and_underrun(void)
{
    fixture(512); start_ready();
    uint8_t raw[640];
    const int volumes[] = {-20, 25, 120};
    for (size_t trial = 0; trial < 3; ++trial) {
        for (size_t i = 0; i < 320; ++i) {
            pcm_write(raw, i, (int16_t)(i % 2 ? -12000 : 8000));
            dma[0][2 * i] = 222; dma[0][2 * i + 1] = 9999;
        }
        size_t count = trial == 1 ? 7 : 320;
        voice_packet_t p = packet(raw, count, (uint32_t)trial);
        uint32_t first = s_cycles;
        assert(echo_reference_lookup(s_reference, first) == NULL);
        assert(audio_play_queue(&p, volumes[trial]));
        /* Merely queued PCM is not yet a reference or completion credit. */
        assert(echo_reference_lookup(s_reference, first) == NULL);
        audio_render_t render;
        assert(!audio_render_poll(&render));
        host_now += 20000; send_dma(0);
        const int16_t *ref = echo_reference_lookup(s_reference, first);
        assert(ref);
        for (size_t i = 0; i < 320; ++i) {
            assert(ref[i] == 222); /* pre-refill DMA, left mono channel */
            int volume = volumes[trial] < 0 ? 0 : volumes[trial] > 100 ? 100 : volumes[trial];
            int16_t expected = i < count ? (int16_t)((i % 2 ? -12000 : 8000) * volume / 100) : 0;
            assert(dma[0][2 * i] == expected && dma[0][2 * i + 1] == expected);
        }
        assert(!audio_render_poll(&render));
        for (unsigned i = 1; i < 4; ++i) { host_now += 20000; send_dma(i); }
        assert(!audio_render_poll(&render));
        host_now += 20000; send_dma(0);
        ref = echo_reference_lookup(s_reference, first + 4);
        assert(ref && audio_render_poll(&render));
        assert(render.epoch == 42 && render.turn == 7 && render.seq == trial);
        assert(render.samples == count && render.level >= 0.0f);
        for (size_t i = 0; i < 320; ++i) {
            int volume = volumes[trial] < 0 ? 0 : volumes[trial] > 100 ? 100 : volumes[trial];
            int16_t expected = i < count ? (int16_t)((i % 2 ? -12000 : 8000) * volume / 100) : 0;
            assert(ref[i] == expected);
            assert(dma[0][2 * i] == 0 && dma[0][2 * i + 1] == 0);
        }
        for (unsigned i = 1; i < 4; ++i) { host_now += 20000; send_dma(i); }
        host_now += 20000; send_dma(0);
        ref = echo_reference_lookup(s_reference, first + 8);
        assert(ref && !audio_render_poll(&render));
        for (size_t i = 0; i < 320; ++i) assert(ref[i] == 0);
    }
    puts("ok: actual completed DMA, volume clamps, short padding, underrun, completion credits");
}

static void test_cancel_restart_and_pa_failures(void)
{
    fixture(512);
    assert(audio_play_start(0, 7) == ESP_ERR_INVALID_STATE);
    assert(audio_play_start(42, 7) == ESP_OK);
    assert(audio_play_poll_start() == ESP_ERR_NOT_FINISHED);
    audio_play_abort();
    for (unsigned i = 0; i < 4; ++i) send_next();
    audio_render_t render;
    for (unsigned i = 0; i < 8; ++i) assert(!audio_render_poll(&render));
    assert(!host_pa && host_pa_on_calls == 0);
    assert(audio_play_poll_start() == ESP_ERR_INVALID_STATE);
    assert(host_waits == 0 && host_delays == 0);
    /* A new start owns a new complete four-descriptor flush. */
    assert(audio_play_start(42, 8) == ESP_OK);
    for (unsigned i = 0; i < 3; ++i) send_next();
    assert(audio_play_poll_start() == ESP_ERR_NOT_FINISHED);
    send_next();
    assert(!audio_render_poll(&render) && !host_pa);
    assert(audio_play_poll_start() == ESP_OK && host_pa);
    uint8_t raw[640] = {0};
    voice_packet_t p = packet(raw, 320, 0);
    assert(audio_play_queue(&p, 100));
    send_next(); /* Old generation is now resident in a DMA slot. */
    assert(audio_play_queue(&p, 100)); /* and pending in the CPU queue */
    audio_play_abort();
    unsigned enabled = host_pa_on_calls;
    for (unsigned i = 0; i < 8; ++i) send_next();
    assert(!audio_render_poll(&render));
    assert(!host_pa && host_pa_on_calls == enabled);
    assert(audio_play_poll_start() == ESP_ERR_INVALID_STATE);
    assert(audio_play_start(42, 9) == ESP_OK);
    host_now += 200000;
    assert(audio_play_poll_start() == ESP_ERR_TIMEOUT);
    for (unsigned i = 0; i < 4; ++i) send_next();
    assert(!audio_render_poll(&render) && !host_pa);
    assert(audio_play_poll_start() == ESP_ERR_INVALID_STATE);
    start_ready();
    host_pa_off_error = -77;
    enabled = host_pa_on_calls;
    assert(audio_play_start(42, 10) == -77);
    assert(!s_start_pending && host_pa);
    for (unsigned i = 0; i < 4; ++i) send_next();
    assert(!audio_render_poll(&render));
    assert(audio_play_poll_start() == ESP_ERR_INVALID_STATE);
    assert(host_pa_on_calls == enabled);
    host_pa_off_error = 0; audio_play_abort();
    assert(audio_play_start(42, 11) == ESP_OK);
    for (unsigned i = 0; i < 4; ++i) send_next();
    host_pa_on_error = -78;
    assert(audio_play_poll_start() == -78 && !host_pa);
    assert(audio_play_poll_start() == ESP_ERR_INVALID_STATE);
    host_pa_on_error = 0;
    assert(host_waits == 0 && host_delays == 0);
    puts("ok: nonblocking cancel/restart, stale credits, PA-off failure, start timeout/enable failure");
}

static void test_pa_transition_and_repeated_abort(void)
{
    fixture(512); start_ready();
    int64_t start = host_now;
    for (size_t i = 0; i < 640; ++i) dma[0][i] = 1000;
    host_now = start + 10000; audio_play_abort();
    assert(s_amp_changed_us == start + 10000);
    host_now = start + 15000; audio_play_abort();
    assert(s_amp_changed_us == start + 10000);
    uint32_t sequence = s_cycles;
    host_now = start + 20000; send_dma(0);
    const int16_t *ref = echo_reference_lookup(s_reference, sequence);
    assert(ref);
    for (size_t i = 0; i < 320; ++i) assert(ref[i] == (i < 160 ? 1000 : 0));
    puts("ok: per-sample PA transition and repeated mute preserves transition timestamp");
}

static int16_t capture_sample(size_t index) { return (int16_t)(5000 + index % 500); }
static int16_t reference_sample(size_t index) { return (int16_t)(100 + index % 700); }
static int16_t processed_sample(size_t index) { return (int16_t)(capture_sample(index) - reference_sample(index) + 17); }

static void schedule_cycle(void)
{
    int16_t rx[640];
    host_now += schedule_interval;
    unsigned slot = (unsigned)(schedule_tick % 4);
    for (size_t i = 0; i < 320; ++i) {
        size_t index = schedule_tick * 320 + i;
        rx[2 * i] = (int16_t)(capture_sample(index) - 20);
        rx[2 * i + 1] = (int16_t)(capture_sample(index) + 20);
        dma[slot][2 * i] = reference_sample(index);
        dma[slot][2 * i + 1] = -9999;
    }
    i2s_event_data_t event = {.dma_buf = rx, .size = sizeof(rx)};
    assert(host_rx.enabled && host_rx.callbacks.on_recv);
    /* Exercise both interrupt orders; received() deliberately delays delivery. */
    if (schedule_tick % 2) host_rx.callbacks.on_recv(&host_rx, &event, host_rx.ctx);
    if (!schedule_no_tx) {
        send_dma(slot);
        if (schedule_extra_tx) {
            send_dma((slot + 1) % 4);
            schedule_extra_tx = false;
        }
    }
    if (!(schedule_tick % 2)) host_rx.callbacks.on_recv(&host_rx, &event, host_rx.ctx);
    ++schedule_tick;
}

static void provide_capture(void)
{
    for (unsigned attempt = 0; !s_captured->count && attempt < 3; ++attempt) schedule_cycle();
}

static void capture_fixture(size_t native_size)
{
    fixture(native_size); start_ready();
    host_wait_hook = provide_capture;
}

static void check_capture(size_t offset)
{
    int16_t out[320];
    assert(audio_capture(out) == ESP_OK);
    for (size_t i = 0; i < 320; ++i) assert(out[i] == processed_sample(offset + i));
}

static void test_capture_native_reblocking(bool wrap)
{
    capture_fixture(512);
    if (wrap) s_rx_seq = UINT32_MAX - 2u;
    unsigned creates = host_aec_creates;
    for (size_t block = 0; block < 30; ++block) check_capture(block * 320);
    assert(host_aec_creates == creates + 1); /* first call rebuilds, then stays native */
    assert(host_aec_count >= 30 * 320 && host_aec_calls * 512 == host_aec_count);
    for (size_t i = 0; i < host_aec_count; ++i) {
        assert(host_aec_mic[i] == capture_sample(i));
        assert(host_aec_ref[i] == reference_sample(i));
    }
    puts(wrap ? "ok: capture sequence wrap preserves RX/TX pairing" :
                "ok: real RX/TX callbacks, both ISR orders, native 512 to network 320 without loss");
}

static void expect_capture_failure(int error, size_t previous_calls)
{
    int16_t out[320];
    for (size_t i = 0; i < 320; ++i) out[i] = -12345;
    assert(audio_capture(out) == error);
    for (size_t i = 0; i < 320; ++i) assert(out[i] == -12345);
    assert(host_aec_calls == previous_calls && !s_capture_started);
}

static void test_capture_rejects_unsynchronized_audio(void)
{
    capture_fixture(320); check_capture(0);
    unsigned creates = host_aec_creates;
    host_now += 120000; /* A scheduler/IRQ stall is not a configuration resume. */
    check_capture(320); /* one already completed, delayed RX block remains valid */
    expect_capture_failure(ESP_ERR_INVALID_SIZE, 2);
    assert(host_aec_creates == creates);
    capture_fixture(320);
    schedule_no_tx = true;
    expect_capture_failure(ESP_ERR_INVALID_STATE, 0);
    capture_fixture(320); check_capture(0);
    /* Skip a receive sequence, retaining a valid timestamp cadence. */
    ++s_rx_seq;
    check_capture(320);
    expect_capture_failure(ESP_ERR_INVALID_SIZE, 2);
    const int64_t intervals[] = {9999, 30001};
    for (size_t trial = 0; trial < 2; ++trial) {
        capture_fixture(320); check_capture(0);
        schedule_interval = intervals[trial];
        check_capture(320); /* one-frame delayed prior valid block */
        expect_capture_failure(ESP_ERR_INVALID_SIZE, 2);
    }
    capture_fixture(320); check_capture(0);
    schedule_no_tx = true;
    check_capture(320);
    expect_capture_failure(ESP_ERR_INVALID_STATE, 2);
    /* An extra TX EOF at a valid time changes nearest seq without an RX gap. */
    capture_fixture(320); check_capture(0);
    schedule_extra_tx = true;
    check_capture(320);
    expect_capture_failure(ESP_ERR_INVALID_STATE, 2);
    capture_fixture(320);
    host_wait_hook = NULL;
    expect_capture_failure(ESP_ERR_TIMEOUT, 0);
    capture_fixture(320);
    host_aec_fail_create = true;
    expect_capture_failure(ESP_ERR_NO_MEM, 0);
    host_aec_fail_create = false;
    puts("ok: missing/mismatched reference, RX gap, timestamp cadence, timeout and AEC failure never return raw");
}

static void capture_during_aec_creation(void)
{
    host_aec_create_hook = NULL;
    schedule_cycle();
    schedule_cycle();
}

static void test_capture_reset_discards_queued_output(void)
{
    capture_fixture(512); check_capture(0);
    assert(echo_stream_available(s_echo) == 192);
    assert(schedule_tick == 3);
    schedule_cycle(); /* Queue block 2, retained previous RX is block 3. */
    assert(s_captured->count == 1);
    unsigned creates = host_aec_creates;
    audio_capture_reset();
    check_capture(4 * 320);
    assert(host_aec_creates == creates + 1);
    assert(host_aec_destroys == host_aec_creates - 1);
    /* Old output, queued block 2, held previous block 3, and native tail are gone. */
    for (size_t i = 0; i < 512; ++i) {
        assert(host_aec_mic[512 + i] == capture_sample(1280 + i));
        assert(host_aec_ref[512 + i] == reference_sample(1280 + i));
    }
    /* Native AEC creation can take time while the real ISR keeps capturing.
     * A cutoff recorded before creation would accept the held old block here. */
    size_t fresh_offset = (schedule_tick + 2) * 320;
    size_t previous_count = host_aec_count;
    host_aec_create_hook = capture_during_aec_creation;
    audio_capture_reset();
    check_capture(fresh_offset);
    assert(host_aec_create_hook == NULL);
    for (size_t i = 0; i < 512; ++i) {
        assert(host_aec_mic[previous_count + i] == capture_sample(fresh_offset + i));
        assert(host_aec_ref[previous_count + i] == reference_sample(fresh_offset + i));
    }
    puts("ok: capture reset drops queued/held RX and old native output, including audio during AEC creation");
}

int main(void)
{
    test_played_reference_volume_and_underrun();
    test_cancel_restart_and_pa_failures();
    test_pa_transition_and_repeated_abort();
    test_capture_native_reblocking(false);
    test_capture_native_reblocking(true);
    test_capture_rejects_unsynchronized_audio();
    test_capture_reset_discards_queued_output();
    host_wait_hook = NULL; cleanup();
    assert(host_aec_creates == host_aec_destroys);
    assert(!s_pending && !s_rendered && !s_captured && !s_aec && !s_echo && !s_reference);
    puts("audio_test: all tests passed (real audio.c + echo_stream.c)");
    return 0;
}
