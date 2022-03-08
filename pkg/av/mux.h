#ifndef MUX_H
#define MUX_H

#include <stdint.h>

extern int goWriteRTPPacketFunc(void *, uint8_t *, int);

int cgoWriteRTPPacketFunc(void *, uint8_t *, int);

#endif