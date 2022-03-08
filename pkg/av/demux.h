#ifndef DEMUX_H
#define DEMUX_H

#include <stdint.h>

extern int goReadBufferFunc(void *, uint8_t *, int);
extern int goWriteRTCPPacketFunc(void *, uint8_t *, int);

int cgoReadBufferFunc(void *, uint8_t *, int);
int cgoWriteRTCPPacketFunc(void *, uint8_t *, int);

#endif