package buffer

import (
	"sync"
)

type ReceiveLog struct {
	packets         []uint64
	size            uint16
	end             uint16
	started         bool
	lastConsecutive uint16
	m               sync.RWMutex
}

const uint16SizeHalf = 1 << 15

func NewReceiveLog(size uint16) *ReceiveLog {
	return &ReceiveLog{
		packets: make([]uint64, size/64),
		size:    size,
	}
}

func (s *ReceiveLog) Add(seq uint16) {
	s.m.Lock()
	defer s.m.Unlock()

	if !s.started {
		s.SetReceived(seq)
		s.end = seq
		s.started = true
		s.lastConsecutive = seq
		return
	}

	diff := seq - s.end
	switch {
	case diff == 0:
		return
	case diff < uint16SizeHalf:
		// this means a positive diff, in other words seq > end (with counting for rollovers)
		for i := s.end + 1; i != seq; i++ {
			// clear packets between end and seq (these may contain packets from a "size" ago)
			s.DelReceived(i)
		}
		s.end = seq

		if s.lastConsecutive+1 == seq {
			s.lastConsecutive = seq
		} else if seq-s.lastConsecutive > s.size {
			s.lastConsecutive = seq - s.size
			s.FixLastConsecutive() // there might be valid packets at the beginning of the buffer now
		}
	case s.lastConsecutive+1 == seq:
		// negative diff, seq < end (with counting for rollovers)
		s.lastConsecutive = seq
		s.FixLastConsecutive() // there might be other valid packets after seq
	}

	s.SetReceived(seq)
}

func (s *ReceiveLog) get(seq uint16) bool {
	s.m.RLock()
	defer s.m.RUnlock()

	diff := s.end - seq
	if diff >= uint16SizeHalf {
		return false
	}

	if diff >= s.size {
		return false
	}

	return s.GetReceived(seq)
}

func (s *ReceiveLog) MissingSeqNumbers(skipLastN uint16) []uint16 {
	s.m.RLock()
	defer s.m.RUnlock()

	until := s.end - skipLastN
	if until-s.lastConsecutive >= uint16SizeHalf {
		// until < s.lastConsecutive (counting for rollover)
		return nil
	}

	missingPacketSeqNums := make([]uint16, 0)
	for i := s.lastConsecutive + 1; i != until+1; i++ {
		if !s.GetReceived(i) {
			missingPacketSeqNums = append(missingPacketSeqNums, i)
		}
	}

	return missingPacketSeqNums
}

func (s *ReceiveLog) SetReceived(seq uint16) {
	pos := seq % s.size
	s.packets[pos/64] |= 1 << (pos % 64)
}

func (s *ReceiveLog) DelReceived(seq uint16) {
	pos := seq % s.size
	s.packets[pos/64] &^= 1 << (pos % 64)
}

func (s *ReceiveLog) GetReceived(seq uint16) bool {
	pos := seq % s.size
	return (s.packets[pos/64] & (1 << (pos % 64))) != 0
}

func (s *ReceiveLog) FixLastConsecutive() {
	i := s.lastConsecutive + 1
	for ; i != s.end+1 && s.GetReceived(i); i++ {
		// find all consecutive packets
	}
	s.lastConsecutive = i - 1
}
