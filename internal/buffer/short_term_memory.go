package buffer

import "github.com/pion/rtp"

type shortTermMemory [1 << 16]*rtp.Packet

func id(p *rtp.Packet) uint16 {
	return p.SequenceNumber ^ (uint16(p.Timestamp)) ^ (uint16(p.Timestamp >> 16))
}

// Insert inserts a packet into the short term memory circular buffer.
func (s *shortTermMemory) Insert(p *rtp.Packet) {
	s[id(p)] = p
}

// Contains checks if the packet is in the short term memory circular buffer, comparing by timestamp and seq number.
func (s *shortTermMemory) Contains(p *rtp.Packet) bool {
	q := s[id(p)]
	return q != nil && q.Timestamp == p.Timestamp && q.SequenceNumber == p.SequenceNumber
}
