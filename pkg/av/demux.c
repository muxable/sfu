#include "demux.h"

#include <stdint.h>

int cgoReadBufferFunc(void *opaque, uint8_t *buf, int buf_size)
{
    return goReadBufferFunc(opaque, buf, buf_size);
}

int cgoWriteRTCPPacketFunc(void *opaque, uint8_t *buf, int buf_size)
{
    return goWriteRTCPPacketFunc(opaque, buf, buf_size);
}