#include "echo_stream.h"

#include <string.h>

void echo_reference_init(echo_reference_t *reference)
{
    if (reference != NULL) {
        memset(reference, 0, sizeof(*reference));
    }
}

bool echo_reference_push(echo_reference_t *reference,
                         const int16_t mono[ECHO_REFERENCE_BLOCK_SAMPLES],
                         uint32_t sequence, int64_t end_us)
{
    if (reference == NULL || mono == NULL) {
        return false;
    }
    echo_reference_block_t *block = &reference->blocks[reference->next];
    memcpy(block->mono, mono, sizeof(block->mono));
    block->sequence = sequence;
    block->end_us = end_us;
    reference->next = (reference->next + 1u) % ECHO_REFERENCE_HISTORY_BLOCKS;
    if (reference->count < ECHO_REFERENCE_HISTORY_BLOCKS) {
        ++reference->count;
    }
    return true;
}

const int16_t *echo_reference_lookup(const echo_reference_t *reference,
                                     uint32_t sequence)
{
    if (reference == NULL) {
        return NULL;
    }
    for (size_t age = 0; age < reference->count; ++age) {
        size_t index = (reference->next + ECHO_REFERENCE_HISTORY_BLOCKS - 1u
                        - age) % ECHO_REFERENCE_HISTORY_BLOCKS;
        if (reference->blocks[index].sequence == sequence) {
            return reference->blocks[index].mono;
        }
    }
    return NULL;
}

bool echo_reference_find(const echo_reference_t *reference, int64_t capture_us,
                         uint32_t *sequence)
{
    if (reference == NULL || sequence == NULL) {
        return false;
    }
    uint64_t closest = (uint64_t)ECHO_REFERENCE_TOLERANCE_US + 1u;
    uint32_t found_sequence = 0;
    for (size_t age = 0; age < reference->count; ++age) {
        size_t index = (reference->next + ECHO_REFERENCE_HISTORY_BLOCKS - 1u
                        - age) % ECHO_REFERENCE_HISTORY_BLOCKS;
        const echo_reference_block_t *block = &reference->blocks[index];
        /* Unsigned subtraction avoids overflow even for INT64_MIN/MAX. */
        uint64_t distance = block->end_us >= capture_us
            ? (uint64_t)block->end_us - (uint64_t)capture_us
            : (uint64_t)capture_us - (uint64_t)block->end_us;
        if (distance < closest) {
            closest = distance;
            found_sequence = block->sequence;
        }
    }
    if (closest > (uint64_t)ECHO_REFERENCE_TOLERANCE_US) {
        return false;
    }
    *sequence = found_sequence;
    return true;
}

bool echo_stream_init(echo_stream_t *stream, size_t frame_size,
                      echo_stream_process_fn process, void *ctx)
{
    if (stream == NULL) {
        return false;
    }
    memset(stream, 0, sizeof(*stream));
    if (frame_size == 0 || frame_size > ECHO_STREAM_MAX_FRAME_SAMPLES) {
        return false;
    }
    stream->frame_size = frame_size;
    stream->process = process;
    stream->ctx = ctx;
    return true;
}

bool echo_stream_feed(echo_stream_t *stream, const int16_t *mic,
                      const int16_t *ref, size_t count)
{
    if (stream == NULL || stream->frame_size == 0 || stream->process == NULL
        || count > ECHO_REFERENCE_BLOCK_SAMPLES
        || (count != 0 && (mic == NULL || ref == NULL))) {
        return false;
    }
    size_t produced = ((stream->pending + count) / stream->frame_size)
                      * stream->frame_size;
    if (produced > ECHO_STREAM_FIFO_SAMPLES - stream->fifo_count) {
        return false;
    }
    size_t consumed = 0;
    while (consumed < count) {
        size_t take = stream->frame_size - stream->pending;
        if (take > count - consumed) {
            take = count - consumed;
        }
        memcpy(stream->mic + stream->pending, mic + consumed,
               take * sizeof(*mic));
        memcpy(stream->ref + stream->pending, ref + consumed,
               take * sizeof(*ref));
        stream->pending += take;
        consumed += take;
        if (stream->pending == stream->frame_size) {
            stream->process(stream->ctx, stream->mic, stream->ref,
                            stream->processed);
            for (size_t i = 0; i < stream->frame_size; ++i) {
                size_t tail = (stream->fifo_head + stream->fifo_count)
                              % ECHO_STREAM_FIFO_SAMPLES;
                stream->fifo[tail] = stream->processed[i];
                ++stream->fifo_count;
            }
            stream->pending = 0;
        }
    }
    return true;
}

bool echo_stream_read(echo_stream_t *stream, int16_t *out, size_t count)
{
    if (stream == NULL || count > stream->fifo_count
        || (count != 0 && out == NULL)) {
        return false;
    }
    for (size_t i = 0; i < count; ++i) {
        out[i] = stream->fifo[stream->fifo_head];
        stream->fifo_head = (stream->fifo_head + 1u) % ECHO_STREAM_FIFO_SAMPLES;
    }
    stream->fifo_count -= count;
    return true;
}

size_t echo_stream_available(const echo_stream_t *stream)
{
    return stream == NULL ? 0 : stream->fifo_count;
}
