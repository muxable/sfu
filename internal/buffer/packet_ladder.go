package buffer

import "github.com/pion/rtp"

type packetLadder32 struct {
	RefTimestamp uint64
}

const PacketLadderOffset32 = 1<<31

func (l *packetLadder32) AbsTimestamp(p *rtp.Packet) uint64 {
	ts := uint64(p.Timestamp) + PacketLadderOffset32
	if l.RefTimestamp == 0 {
		return ts
	} 
	
	// calculate the true timestamp
	tsn := ts
	tsm := l.RefTimestamp

	if tsn > tsm && tsn-tsm < PacketLadderOffset32 {
		tsm += uint64(tsn - tsm)
	} else if tsn < tsm && tsm-tsn >= PacketLadderOffset32 {
		tsm += 1<<32 - uint64(tsm-tsn)
	} else if tsn > tsm && tsn-tsm >= PacketLadderOffset32 {
		tsm -= 1<<32 - uint64(tsn-tsm)
	} else if tsn < tsm && tsm-tsn < PacketLadderOffset32 {
		tsm -= uint64(tsm - tsn)
	}
	return tsm
}
