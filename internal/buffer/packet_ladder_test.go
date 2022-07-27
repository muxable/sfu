package buffer

import (
	"testing"

	"github.com/pion/rtp"
)

func TestPacketLadder_Simple(t *testing.T) {
	p0 := &rtp.Packet{Header: rtp.Header{Timestamp: 0}}
	p1 := &rtp.Packet{Header: rtp.Header{Timestamp: 1}}
	p2 := &rtp.Packet{Header: rtp.Header{Timestamp: 2}}
	p3 := &rtp.Packet{Header: rtp.Header{Timestamp: 3}}

	l := &packetLadder32{}

	t0 := l.AbsTimestamp(p0)
	if t0 != 0 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0+PacketLadderOffset32, t0)
	}
	
	t1 := l.AbsTimestamp(p1)
	if t1 != 1 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 1+PacketLadderOffset32, t1)
	}

	t2 := l.AbsTimestamp(p2)
	if t2 != 2 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 2+PacketLadderOffset32, t2)
	}

	t3 := l.AbsTimestamp(p3)
	if t3 != 3 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 3+PacketLadderOffset32, t3)
	}
}

func TestPacketLadder_SetRefTimestamp(t *testing.T) {
	p0 := &rtp.Packet{Header: rtp.Header{Timestamp: 0}}
	p1 := &rtp.Packet{Header: rtp.Header{Timestamp: 1}}
	p2 := &rtp.Packet{Header: rtp.Header{Timestamp: 2}}
	p3 := &rtp.Packet{Header: rtp.Header{Timestamp: 3}}

	l := &packetLadder32{}

	t0 := l.AbsTimestamp(p0)
	if t0 != 0 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0+PacketLadderOffset32, t0)
	}
	l.RefTimestamp = t0

	t1 := l.AbsTimestamp(p1)
	if t1 != 1 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 1+PacketLadderOffset32, t1)
	}
	l.RefTimestamp = t1

	t2 := l.AbsTimestamp(p2)
	if t2 != 2 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 2+PacketLadderOffset32, t2)
	}
	l.RefTimestamp = t2

	t3 := l.AbsTimestamp(p3)
	if t3 != 3 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 3+PacketLadderOffset32, t3)
	}
}

func TestPacketLadder_Rollover(t *testing.T) {
	p0 := &rtp.Packet{Header: rtp.Header{Timestamp: 0xFFFFFFFE}}
	p1 := &rtp.Packet{Header: rtp.Header{Timestamp: 0xFFFFFFFF}}
	p2 := &rtp.Packet{Header: rtp.Header{Timestamp: 0x00000000}}
	p3 := &rtp.Packet{Header: rtp.Header{Timestamp: 0x00000001}}

	l := &packetLadder32{}

	t0 := l.AbsTimestamp(p0)
	if t0 != 0xFFFFFFFE + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0xFFFFFFFE+PacketLadderOffset32, t0)
	}
	l.RefTimestamp = t0

	t1 := l.AbsTimestamp(p1)
	if t1 != 0xFFFFFFFF + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0xFFFFFFFF+PacketLadderOffset32, t1)
	}

	t2 := l.AbsTimestamp(p2)
	if t2 != 0x100000000 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0x100000000+PacketLadderOffset32, t2)
	}

	t3 := l.AbsTimestamp(p3)
	if t3 != 0x100000001 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0x100000001+PacketLadderOffset32, t3)
	}
}

func TestPacketLadder_RolloverReverse(t *testing.T) {
	p0 := &rtp.Packet{Header: rtp.Header{Timestamp: 0xFFFFFFFE}}
	p1 := &rtp.Packet{Header: rtp.Header{Timestamp: 0x00000000}}
	p2 := &rtp.Packet{Header: rtp.Header{Timestamp: 0xFFFFFFFF}}
	p3 := &rtp.Packet{Header: rtp.Header{Timestamp: 0x00000001}}

	l := &packetLadder32{}

	t0 := l.AbsTimestamp(p0)
	if t0 != 0xFFFFFFFE + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0xFFFFFFFE+PacketLadderOffset32, t0)
	}
	l.RefTimestamp = t0

	t1 := l.AbsTimestamp(p1)
	if t1 != 0x100000000 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0x00000000+PacketLadderOffset32, t1)
	}
	l.RefTimestamp = t1

	t2 := l.AbsTimestamp(p2)
	if t2 != 0xFFFFFFFF + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0xFFFFFFFF+PacketLadderOffset32, t2)
	}
	l.RefTimestamp = t2

	t3 := l.AbsTimestamp(p3)
	if t3 != 0x100000001 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0x100000001+PacketLadderOffset32, t3)
	}
}

func TestPacketLadder_LargeJump(t *testing.T) {
	p0 := &rtp.Packet{Header: rtp.Header{Timestamp: 0x00000000}}
	p1 := &rtp.Packet{Header: rtp.Header{Timestamp: 0xFFFFFFFF}}

	l := &packetLadder32{}

	t0 := l.AbsTimestamp(p0)
	if t0 != 0x00000000 + PacketLadderOffset32 {
		t.Errorf("incorrect timestamp, want %d got %d", 0x00000000+PacketLadderOffset32, t0)
	}
	l.RefTimestamp = t0

	t1 := l.AbsTimestamp(p1)
	if t1 != 0x00000000 + PacketLadderOffset32 - 1 {
		t.Errorf("incorrect timestamp, want %d got %d", 0x00000000+PacketLadderOffset32-1, t1)
	}
	l.RefTimestamp = t1
}