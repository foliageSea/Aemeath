#ifndef ECHO_STREAM_H
#define ECHO_STREAM_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define ECHO_REFERENCE_BLOCK_SAMPLES 320u
#define ECHO_REFERENCE_HISTORY_BLOCKS 16u
#define ECHO_REFERENCE_TOLERANCE_US 10000
#define ECHO_STREAM_MAX_FRAME_SAMPLES 512u
#define ECHO_STREAM_FIFO_SAMPLES 1024u

/* Pure C11, allocation-free helpers. The caller serializes every operation,
 * including use of pointers returned by lookup. No internal locking is done. */
typedef struct {
    int16_t mono[ECHO_REFERENCE_BLOCK_SAMPLES];
    uint32_t sequence;
    int64_t end_us;
} echo_reference_block_t;

typedef struct {
    echo_reference_block_t blocks[ECHO_REFERENCE_HISTORY_BLOCKS];
    size_t next;
    size_t count;
} echo_reference_t;

/* Reset history. Only push actual completed playback, with its completion time. */
void echo_reference_init(echo_reference_t *reference);
bool echo_reference_push(echo_reference_t *reference,
                         const int16_t mono[ECHO_REFERENCE_BLOCK_SAMPLES],
                         uint32_t sequence, int64_t end_us);
/* Exact sequence equality handles uint32 wrap. Newest duplicate wins. Returned
 * storage belongs to reference and may be overwritten by a subsequent push. */
const int16_t *echo_reference_lookup(const echo_reference_t *reference,
                                     uint32_t sequence);
/* Select the closest end_us within inclusive +/-10ms of capture_us. A tie
 * selects the most recently pushed block. On failure, *sequence is unchanged. */
bool echo_reference_find(const echo_reference_t *reference, int64_t capture_us,
                         uint32_t *sequence);

/* The callback must write frame_size output samples, synchronously. Its three
 * sample buffers are distinct and 16-byte aligned. It may modify mic/ref.
 * It must not reenter this stream; ctx and its AEC state remain caller-owned. */
typedef void (*echo_stream_process_fn)(void *ctx, int16_t *mic, int16_t *ref,
                                       int16_t *out);

typedef struct {
    _Alignas(16) int16_t mic[ECHO_STREAM_MAX_FRAME_SAMPLES];
    _Alignas(16) int16_t ref[ECHO_STREAM_MAX_FRAME_SAMPLES];
    _Alignas(16) int16_t processed[ECHO_STREAM_MAX_FRAME_SAMPLES];
    int16_t fifo[ECHO_STREAM_FIFO_SAMPLES];
    size_t frame_size;
    size_t pending;
    size_t fifo_head;
    size_t fifo_count;
    echo_stream_process_fn process;
    void *ctx;
} echo_stream_t;

/* Initialize/reset, discarding queued output and incomplete input. Valid native
 * frame sizes are 1..512 samples. Invalid size leaves the stream disabled.
 * A NULL callback is allowed but feed then fails: raw mic is never substituted.
 * Ensure 16-byte object alignment (automatic/static C objects satisfy this). */
bool echo_stream_init(echo_stream_t *stream, size_t frame_size,
                      echo_stream_process_fn process, void *ctx);
/* Accept 0..320 paired samples; arbitrary chunk boundaries are supported.
 * All complete native frames are processed and queued immediately; incomplete
 * frames wait for the next feed. No implicit padding or tail flushing occurs.
 * On invalid arguments, missing AEC, or insufficient output FIFO capacity,
 * return false without consuming any input, changing state, or calling AEC.
 * Input/output arrays must not alias stream storage. */
bool echo_stream_feed(echo_stream_t *stream, const int16_t *mic,
                      const int16_t *ref, size_t count);
/* Read exactly count samples only when all are available. Failure changes
 * neither stream nor output. count==0 allows NULL output. */
bool echo_stream_read(echo_stream_t *stream, int16_t *out, size_t count);
size_t echo_stream_available(const echo_stream_t *stream);

#endif
