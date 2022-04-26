package ccnack

import (
	"fmt"
	"sync"

	"github.com/pion/rtp"
)

const (
	uint16SizeHalf = 1 << 15
)

type sendBuffer struct {
	packets   []*rtp.Packet
	size      uint16
	lastAdded uint16
	started   bool
	hdrExtID  uint8

	m sync.RWMutex
}

func newSendBuffer(size uint16, hdrExtID uint8) (*sendBuffer, error) {
	allowedSizes := make([]uint16, 0)
	correctSize := false
	for i := 0; i < 16; i++ {
		if size == 1<<i {
			correctSize = true
			break
		}
		allowedSizes = append(allowedSizes, 1<<i)
	}

	if !correctSize {
		return nil, fmt.Errorf("%w: %d is not a valid size, allowed sizes: %v", ErrInvalidSize, size, allowedSizes)
	}

	return &sendBuffer{
		packets:  make([]*rtp.Packet, size),
		size:     size,
		hdrExtID: hdrExtID,
	}, nil
}

func (s *sendBuffer) add(packet *rtp.Packet) {
	s.m.Lock()
	defer s.m.Unlock()

	ext := packet.Header.GetExtension(s.hdrExtID)
	if ext == nil {
		return
	}
	var tccExt rtp.TransportCCExtension
	if err := tccExt.Unmarshal(ext); err != nil {
		return
	}
	seq := tccExt.TransportSequence

	if !s.started {
		s.packets[seq%s.size] = packet
		s.lastAdded = seq
		s.started = true
		return
	}

	diff := seq - s.lastAdded
	if diff == 0 {
		return
	} else if diff < uint16SizeHalf {
		for i := s.lastAdded + 1; i != seq; i++ {
			idx := i % s.size
			s.packets[idx] = nil
		}
	}

	idx := seq % s.size
	s.packets[idx] = packet
	s.lastAdded = seq
}

func (s *sendBuffer) get(seq uint16) *rtp.Packet {
	s.m.RLock()
	defer s.m.RUnlock()

	diff := s.lastAdded - seq
	if diff >= uint16SizeHalf {
		return nil
	}

	if diff >= s.size {
		return nil
	}

	pkt := s.packets[seq%s.size]
	if pkt == nil {
		return nil
	}
	ext := pkt.Header.GetExtension(s.hdrExtID)
	if ext == nil {
		return nil
	}
	var tccExt rtp.TransportCCExtension
	if err := tccExt.Unmarshal(ext); err != nil {
		return nil
	}
	if tccExt.TransportSequence != seq {
		return nil
	}
	return pkt
}
