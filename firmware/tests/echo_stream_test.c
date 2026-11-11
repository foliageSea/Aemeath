#include "../main/audio/echo_stream.h"

#include <assert.h>
#include <stdio.h>
#include <string.h>

_Static_assert(_Alignof(echo_stream_t) >= 16, "native buffer alignment");

typedef struct {
    size_t frame_size;
    size_t processed;
    size_t calls;
} test_aec_t;

static int16_t mic_sample(size_t index)
{
    return (int16_t)(index % 12000u);
}

static int16_t ref_sample(size_t index)
{
    return (int16_t)(-1000 - (int)(index % 7000u));
}

static int16_t output_sample(size_t index)
{
    return (int16_t)(mic_sample(index) - ref_sample(index));
}

static void process(void *ctx, int16_t *mic, int16_t *ref, int16_t *out)
{
    test_aec_t *aec = ctx;
    assert(((uintptr_t)mic % 16u) == 0);
    assert(((uintptr_t)ref % 16u) == 0);
    assert(((uintptr_t)out % 16u) == 0);
    assert(mic != ref && mic != out && ref != out);
    for (size_t i = 0; i < aec->frame_size; ++i) {
        size_t index = aec->processed + i;
        assert(mic[i] == mic_sample(index));
        assert(ref[i] == ref_sample(index));
        out[i] = (int16_t)(mic[i] - ref[i]);
        /* Native implementations may use inputs as scratch. */
        mic[i] = -123;
        ref[i] = 456;
    }
    aec->processed += aec->frame_size;
    ++aec->calls;
}

static void samples(int16_t *mic, int16_t *ref, size_t offset, size_t count)
{
    for (size_t i = 0; i < count; ++i) {
        mic[i] = mic_sample(offset + i);
        ref[i] = ref_sample(offset + i);
    }
}

static void read_checked(echo_stream_t *stream, size_t *offset, size_t count)
{
    int16_t out[ECHO_STREAM_FIFO_SAMPLES];
    assert(count <= ECHO_STREAM_FIFO_SAMPLES);
    assert(echo_stream_read(stream, out, count));
    for (size_t i = 0; i < count; ++i) {
        assert(out[i] == output_sample(*offset + i));
    }
    *offset += count;
}

static void stream_round(size_t frame_size, bool variable_chunks)
{
    echo_stream_t stream;
    test_aec_t aec = {.frame_size = frame_size};
    int16_t mic[320], ref[320];
    size_t fed = 0, read = 0, iteration = 0;
    const size_t total = frame_size * 64u;
    const size_t chunks[] = {320, 1, 319, 17, 193, 256, 7, 128};
    assert(echo_stream_init(&stream, frame_size, process, &aec));
    while (fed < total) {
        size_t count = variable_chunks
            ? chunks[iteration % (sizeof(chunks) / sizeof(chunks[0]))] : 320;
        if (count > total - fed) {
            count = total - fed;
        }
        samples(mic, ref, fed, count);
        assert(echo_stream_feed(&stream, mic, ref, count));
        fed += count;
        /* Request 20ms output even for 32ms native frames. */
        while (echo_stream_available(&stream) >= 320) {
            read_checked(&stream, &read, 320);
        }
        assert(aec.processed == (fed / frame_size) * frame_size);
        assert(echo_stream_available(&stream) == aec.processed - read);
        ++iteration;
    }
    read_checked(&stream, &read, echo_stream_available(&stream));
    assert(read == total && aec.processed == total);
    assert(aec.calls == 64);
    assert(stream.pending == 0 && echo_stream_available(&stream) == 0);
}

static void stream_fifo_boundaries(void)
{
    echo_stream_t stream, before;
    test_aec_t aec = {.frame_size = 512};
    int16_t mic[320], ref[320], sentinel[1025];
    size_t fed = 0, read = 0;
    assert(echo_stream_init(&stream, 512, process, &aec));
    /* Four 20ms blocks produce two 32ms frames and leave 256 pending. */
    for (size_t i = 0; i < 4; ++i) {
        samples(mic, ref, fed, 320);
        assert(echo_stream_feed(&stream, mic, ref, 320));
        fed += 320;
    }
    assert(echo_stream_available(&stream) == 1024);
    assert(stream.pending == 256 && aec.calls == 2);
    /* Pending input can grow while FIFO is full, until it completes a frame. */
    samples(mic, ref, fed, 255);
    assert(echo_stream_feed(&stream, mic, ref, 255));
    fed += 255;
    assert(stream.pending == 511);
    memcpy(&before, &stream, sizeof(stream));
    samples(mic, ref, fed, 1);
    assert(!echo_stream_feed(&stream, mic, ref, 1));
    assert(memcmp(&before, &stream, sizeof(stream)) == 0 && aec.calls == 2);
    for (size_t i = 0; i < 1025; ++i) {
        sentinel[i] = -1234;
    }
    assert(!echo_stream_read(&stream, sentinel, 1025));
    assert(memcmp(&before, &stream, sizeof(stream)) == 0);
    for (size_t i = 0; i < 1025; ++i) {
        assert(sentinel[i] == -1234);
    }
    read_checked(&stream, &read, 511);
    memcpy(&before, &stream, sizeof(stream));
    assert(!echo_stream_feed(&stream, mic, ref, 1));
    assert(memcmp(&before, &stream, sizeof(stream)) == 0 && aec.calls == 2);
    read_checked(&stream, &read, 1);
    assert(echo_stream_feed(&stream, mic, ref, 1));
    ++fed;
    assert(aec.calls == 3 && stream.pending == 0);
    assert(echo_stream_available(&stream) == 1024);
    read_checked(&stream, &read, 1024);
    assert(fed == read && read == 1536);
    /* Reinitialization drops both processed output and incomplete input. */
    samples(mic, ref, fed, 320);
    assert(echo_stream_feed(&stream, mic, ref, 320));
    assert(stream.pending == 320);
    assert(echo_stream_init(&stream, 512, process, &aec));
    assert(stream.pending == 0 && echo_stream_available(&stream) == 0);
    aec.processed = 0;
    samples(mic, ref, 0, 320);
    assert(echo_stream_feed(&stream, mic, ref, 320));
    samples(mic, ref, 320, 192);
    assert(echo_stream_feed(&stream, mic, ref, 192));
    assert(echo_stream_available(&stream) == 512);
    assert(echo_stream_init(&stream, 320, NULL, NULL));
    assert(echo_stream_available(&stream) == 0);
    assert(!echo_stream_feed(&stream, mic, ref, 320));
    assert(!echo_stream_read(&stream, sentinel, 1));
}

static void stream_multi_frame_rejection(void)
{
    echo_stream_t stream, before;
    test_aec_t aec = {.frame_size = 1};
    int16_t mic[320], ref[320];
    size_t read = 0;
    assert(echo_stream_init(&stream, 1, process, &aec));
    for (size_t i = 0; i < 3; ++i) {
        samples(mic, ref, 320 * i, 320);
        assert(echo_stream_feed(&stream, mic, ref, 320));
    }
    samples(mic, ref, 960, 65);
    memcpy(&before, &stream, sizeof(stream));
    assert(!echo_stream_feed(&stream, mic, ref, 65));
    assert(memcmp(&before, &stream, sizeof(stream)) == 0);
    assert(aec.calls == 960);
    assert(echo_stream_feed(&stream, mic, ref, 64));
    assert(echo_stream_available(&stream) == 1024 && aec.calls == 1024);
    read_checked(&stream, &read, 1024);
}

static void invalid_arguments(void)
{
    echo_stream_t stream = {0}, before;
    test_aec_t aec = {.frame_size = 320};
    int16_t mic[321] = {0}, ref[321] = {0}, out = -1234;
    assert(!echo_stream_init(NULL, 320, process, &aec));
    assert(!echo_stream_init(&stream, 0, process, &aec));
    assert(!echo_stream_feed(&stream, mic, ref, 320));
    assert(!echo_stream_init(&stream, 513, process, &aec));
    assert(!echo_stream_feed(&stream, mic, ref, 320));
    assert(echo_stream_init(&stream, 320, process, &aec));
    memcpy(&before, &stream, sizeof(stream));
    assert(!echo_stream_feed(NULL, mic, ref, 1));
    assert(!echo_stream_feed(&stream, NULL, ref, 1));
    assert(!echo_stream_feed(&stream, mic, NULL, 1));
    assert(!echo_stream_feed(&stream, mic, ref, 321));
    assert(!echo_stream_read(NULL, &out, 1));
    assert(!echo_stream_read(&stream, &out, 1));
    assert(!echo_stream_read(&stream, NULL, 1));
    assert(out == -1234 && aec.calls == 0);
    assert(echo_stream_feed(&stream, NULL, NULL, 0));
    assert(echo_stream_read(&stream, NULL, 0));
    assert(memcmp(&before, &stream, sizeof(stream)) == 0);
    assert(echo_stream_available(NULL) == 0);
    assert(echo_stream_init(&stream, 320, NULL, NULL));
    assert(!echo_stream_feed(&stream, mic, ref, 320));
    assert(echo_stream_available(&stream) == 0);
}

static void reference_timestamps(void)
{
    echo_reference_t reference;
    int16_t mono[320] = {0};
    uint32_t seq = 123;
    echo_reference_init(&reference);
    assert(echo_reference_lookup(&reference, 0) == NULL);
    assert(!echo_reference_find(&reference, 0, &seq) && seq == 123);
    assert(echo_reference_push(&reference, mono, 9, 100000));
    assert(echo_reference_find(&reference, 90000, &seq) && seq == 9);
    assert(echo_reference_find(&reference, 110000, &seq) && seq == 9);
    assert(!echo_reference_find(&reference, 89999, &seq) && seq == 9);
    assert(!echo_reference_find(&reference, 110001, &seq) && seq == 9);
    assert(echo_reference_push(&reference, mono, 10, 120000));
    assert(echo_reference_find(&reference, 110000, &seq) && seq == 10);
    assert(echo_reference_find(&reference, 109999, &seq) && seq == 9);
    assert(echo_reference_find(&reference, 110001, &seq) && seq == 10);
    /* Closest timestamp, not sequence ordering or push order. */
    assert(echo_reference_push(&reference, mono, 1, 115000));
    assert(echo_reference_find(&reference, 116000, &seq) && seq == 1);
    echo_reference_init(&reference);
    assert(echo_reference_push(&reference, mono, 0, INT64_MIN));
    assert(!echo_reference_find(&reference, INT64_MAX, &seq));
    assert(echo_reference_find(&reference, INT64_MIN + 10000, &seq) && seq == 0);
    assert(echo_reference_push(&reference, mono, UINT32_MAX, INT64_MAX));
    assert(echo_reference_find(&reference, INT64_MAX - 10000, &seq));
    assert(seq == UINT32_MAX);
    assert(!echo_reference_find(&reference, 0, &seq));
    echo_reference_init(&reference);
    assert(echo_reference_push(&reference, mono, 7, -10000));
    assert(echo_reference_find(&reference, 0, &seq) && seq == 7);
    assert(!echo_reference_find(&reference, 1, &seq));
    assert(echo_reference_push(&reference, mono, 8, 10000));
    assert(echo_reference_find(&reference, 0, &seq) && seq == 8);
    assert(!echo_reference_push(NULL, mono, 0, 0));
    assert(!echo_reference_push(&reference, NULL, 0, 0));
    assert(echo_reference_lookup(NULL, 0) == NULL);
    assert(!echo_reference_find(NULL, 0, &seq));
    assert(!echo_reference_find(&reference, 0, NULL));
    echo_reference_init(NULL);
}

static void reference_history_and_wrap(void)
{
    echo_reference_t reference;
    int16_t mono[320];
    uint32_t first = UINT32_MAX - 7u, seq = 0;
    echo_reference_init(&reference);
    for (size_t i = 0; i < 40; ++i) {
        for (size_t j = 0; j < 320; ++j) {
            mono[j] = (int16_t)(i * 320 + j);
        }
        assert(echo_reference_push(&reference, mono, first + (uint32_t)i,
                                   (int64_t)i * 20000));
        mono[0] = -1; /* push must own a copy. */
        size_t oldest = i >= 15 ? i - 15 : 0;
        assert(reference.count == (i >= 15 ? 16 : i + 1));
        for (size_t k = oldest; k <= i; ++k) {
            const int16_t *found = echo_reference_lookup(&reference,
                                                         first + (uint32_t)k);
            assert(found != NULL);
            for (size_t j = 0; j < 320; ++j) {
                assert(found[j] == (int16_t)(k * 320 + j));
            }
        }
        if (oldest != 0) {
            assert(echo_reference_lookup(&reference,
                       first + (uint32_t)(oldest - 1)) == NULL);
            assert(!echo_reference_find(&reference,
                       (int64_t)(oldest - 1) * 20000, &seq));
        }
        assert(echo_reference_find(&reference, (int64_t)i * 20000, &seq));
        assert(seq == first + (uint32_t)i);
    }
    mono[0] = 234;
    assert(echo_reference_push(&reference, mono, first + 39u, 900000));
    assert(echo_reference_lookup(&reference, first + 39u)[0] == 234);
    echo_reference_init(&reference);
    assert(reference.count == 0 && reference.next == 0);
    assert(echo_reference_lookup(&reference, first + 39u) == NULL);
}

int main(void)
{
    invalid_arguments();
    stream_fifo_boundaries();
    stream_multi_frame_rejection();
    stream_round(512, false); /* 20ms capture / 32ms native / 20ms output. */
    stream_round(320, false);
    for (size_t frame_size = 1; frame_size <= 512; ++frame_size) {
        stream_round(frame_size, true);
    }
    reference_timestamps();
    reference_history_and_wrap();
    puts("echo_stream_test: all tests passed");
    return 0;
}
