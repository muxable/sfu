package buffer

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func newPacket(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: seq,
			Timestamp:      ts,
		},
	}
}

func TestReorderBuffer(t *testing.T) {
	logger := zaptest.NewLogger(t)
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	b := NewReorderBuffer(1, time.Second)
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(3, 0)); err != nil {
		t.Fatal(err)
	}
	p1, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p1.SequenceNumber != 0 {
		t.Errorf("Expected 0, got %d", p1.SequenceNumber)
	}
	p2, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p2.SequenceNumber != 1 {
		t.Errorf("Expected 1, got %d", p2.SequenceNumber)
	}
	p3, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p3.SequenceNumber != 2 {
		t.Errorf("Expected 2, got %d", p3.SequenceNumber)
	}
	p4, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p4.SequenceNumber != 3 {
		t.Errorf("Expected 3, got %d", p4.SequenceNumber)
	}
	if b.Len() != 0 {
		t.Errorf("Expected 0, got %d", b.Len())
	}
}

func TestReorderBuffer_Reordered(t *testing.T) {
	logger := zaptest.NewLogger(t)
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	b := NewReorderBuffer(1, time.Second)
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(3, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(1, 0)); err != nil {
		t.Fatal(err)
	}
	p1, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p1.SequenceNumber != 0 {
		t.Errorf("Expected 0, got %d", p1.SequenceNumber)
	}
	p2, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p2.SequenceNumber != 1 {
		t.Errorf("Expected 1, got %d", p2.SequenceNumber)
	}
	p3, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p3.SequenceNumber != 2 {
		t.Errorf("Expected 2, got %d", p3.SequenceNumber)
	}
	p4, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p4.SequenceNumber != 3 {
		t.Errorf("Expected 3, got %d", p4.SequenceNumber)
	}
	if b.Len() != 0 {
		t.Errorf("Expected 0, got %d", b.Len())
	}
}

func TestReorderBuffer_RemovesDuplicates(t *testing.T) {
	logger := zaptest.NewLogger(t)
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	b := NewReorderBuffer(1, time.Second)
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(1, 0)); err != nil {
		t.Fatal(err)
	}
	p1, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p1.SequenceNumber != 0 {
		t.Errorf("Expected 0, got %d", p1.SequenceNumber)
	}
	p2, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p2.SequenceNumber != 1 {
		t.Errorf("Expected 1, got %d", p2.SequenceNumber)
	}
	if b.Len() != 0 {
		t.Errorf("Expected 0, got %d", b.Len())
	}
}

func TestReorderBuffer_RejectsDuplicateTooLate(t *testing.T) {
	logger := zaptest.NewLogger(t)
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	b := NewReorderBuffer(1, time.Second)
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(1, 0)); err != nil {
		t.Fatal(err)
	}
	p1, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p1.SequenceNumber != 0 {
		t.Errorf("Expected 0, got %d", p1.SequenceNumber)
	}
	p2, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p2.SequenceNumber != 1 {
		t.Errorf("Expected 1, got %d", p2.SequenceNumber)
	}
	if err := b.WriteRTP(newPacket(1, 0)); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 0 {
		t.Errorf("Expected 0, got %d", b.Len())
	}
}

func TestReorderBuffer_MissingSequenceNumbers(t *testing.T) {
	logger := zaptest.NewLogger(t)
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	b := NewReorderBuffer(1, time.Second)
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(3, 0)); err != nil {
		t.Fatal(err)
	}
	missing1 := b.MissingSequenceNumbers()
	if len(missing1) != 2 {
		t.Errorf("Expected 2, got %d", len(missing1))
	}
	if missing1[0] != 1 {
		t.Errorf("Expected 1, got %d", missing1[0])
	}
	if missing1[1] != 2 {
		t.Errorf("Expected 2, got %d", missing1[1])
	}

	p1, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p1.SequenceNumber != 0 {
		t.Errorf("Expected 0, got %d", p1.SequenceNumber)
	}

	missing2 := b.MissingSequenceNumbers()
	if len(missing2) != 2 {
		t.Errorf("Expected 2, got %d", len(missing2))
	}
	if missing2[0] != 1 {
		t.Errorf("Expected 1, got %d", missing2[0])
	}
	if missing2[1] != 2 {
		t.Errorf("Expected 2, got %d", missing2[1])
	}

	if err := b.WriteRTP(newPacket(2, 0)); err != nil {
		t.Fatal(err)
	}

	missing3 := b.MissingSequenceNumbers()
	if len(missing3) != 1 {
		t.Errorf("Expected 1, got %d", len(missing3))
	}
	if missing3[0] != 1 {
		t.Errorf("Expected 1, got %d", missing3[0])
	}

	p2, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p2.SequenceNumber != 2 {
		t.Errorf("Expected 2, got %d", p2.SequenceNumber)
	}

	missing4 := b.MissingSequenceNumbers()
	if len(missing4) != 0 {
		t.Errorf("Expected 0, got %d", len(missing4))
	}

	p3, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p3.SequenceNumber != 3 {
		t.Errorf("Expected 3, got %d", p3.SequenceNumber)
	}

	if b.Len() != 0 {
		t.Errorf("Expected 0, got %d", b.Len())
	}
}

func TestReorderBuffer_RejectsOldPackets(t *testing.T) {
	logger := zaptest.NewLogger(t)
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	b := NewReorderBuffer(1, time.Second)
	if err := b.WriteRTP(newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteRTP(newPacket(3, 0)); err != nil {
		t.Fatal(err)
	}
	missing1 := b.MissingSequenceNumbers()
	if len(missing1) != 2 {
		t.Errorf("Expected 2, got %d", len(missing1))
	}
	if missing1[0] != 1 {
		t.Errorf("Expected 1, got %d", missing1[0])
	}
	if missing1[1] != 2 {
		t.Errorf("Expected 2, got %d", missing1[1])
	}

	p1, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p1.SequenceNumber != 0 {
		t.Errorf("Expected 0, got %d", p1.SequenceNumber)
	}

	missing2 := b.MissingSequenceNumbers()
	if len(missing2) != 2 {
		t.Errorf("Expected 2, got %d", len(missing2))
	}
	if missing2[0] != 1 {
		t.Errorf("Expected 1, got %d", missing2[0])
	}
	if missing2[1] != 2 {
		t.Errorf("Expected 2, got %d", missing2[1])
	}

	if err := b.WriteRTP(newPacket(2, 0)); err != nil {
		t.Fatal(err)
	}

	missing3 := b.MissingSequenceNumbers()
	if len(missing3) != 1 {
		t.Errorf("Expected 1, got %d", len(missing3))
	}
	if missing3[0] != 1 {
		t.Errorf("Expected 1, got %d", missing3[0])
	}

	p2, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p2.SequenceNumber != 2 {
		t.Errorf("Expected 2, got %d", p2.SequenceNumber)
	}

	missing4 := b.MissingSequenceNumbers()
	if len(missing4) != 0 {
		t.Errorf("Expected 0, got %d", len(missing4))
	}

	p3, err := b.ReadRTP()
	if err != nil {
		t.Fatal(err)
	}
	if p3.SequenceNumber != 3 {
		t.Errorf("Expected 3, got %d", p3.SequenceNumber)
	}

	if b.Len() != 0 {
		t.Errorf("Expected 0, got %d", b.Len())
	}
}
