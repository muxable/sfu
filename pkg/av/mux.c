#include "mux.h"

#include <stdint.h>

int cgoWriteRTPPacketFunc(void *opaque, uint8_t *buf, int buf_size)
{
    return goWriteRTPPacketFunc(opaque, buf, buf_size);
}