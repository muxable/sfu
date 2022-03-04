#ifndef DEMUX_H
#define DEMUX_H

#include <stdint.h>

extern int goReadPacketFunc(void *, uint8_t *, int);
extern int goWritePacketFunc(void *, uint8_t *, int);

int cgoReadPacketFunc(void *, uint8_t *, int);
int cgoWritePacketFunc(void *, uint8_t *, int);

#endif