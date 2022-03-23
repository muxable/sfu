package buffer

import (
	"context"
	"sync"
	"time"

	"github.com/pion/rtp"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
)

type BufferedPacket struct {
	*rtp.Packet
	EmitTime time.Time
	AbsTsSeq uint64
}

type ReorderBuffer struct {
	sync.RWMutex

	clockRate         uint32
	delay             time.Duration
	buffer            [1 << 16]*BufferedPacket
	prevTimestamp     uint32
	absTimestamp      uint64
	minAbsTsSeq       uint64
	startRTPTimestamp uint64
	startNTPTimestamp time.Time
	count             uint16

	initialized bool

	evict  uint16
	insert *semaphore.Weighted
}

// NewJitterBuffer creates a new singular jitter buffer with the given context && delay.
// tsClock is a clock that emits at clockRate Hz for the codec.
// delay is in RTP timestamp units.
func NewReorderBuffer(clockRate uint32, delay time.Duration) *ReorderBuffer {
	return &ReorderBuffer{
		clockRate: clockRate,
		delay:     delay,
		insert:    semaphore.NewWeighted(1),
	}
}

func (b *ReorderBuffer) WriteRTP(p *rtp.Packet) error {
	if !b.initialized {
		b.prevTimestamp = p.Timestamp
		b.absTimestamp = uint64(p.Timestamp)
		b.startRTPTimestamp = uint64(p.Timestamp)
		b.startNTPTimestamp = time.Now()
		b.evict = p.SequenceNumber
		b.initialized = true
	} else {
		// calculate the true timestamp
		tsn := p.Timestamp
		tsm := b.prevTimestamp

		pAbsTimestamp := b.absTimestamp

		if tsn > tsm && tsn-tsm < 1<<31 {
			pAbsTimestamp += uint64(tsn - tsm)
		} else if tsn < tsm && tsm-tsn >= 1<<31 {
			pAbsTimestamp += 1<<32 - uint64(tsm-tsn)
		} else if tsn > tsm && tsn-tsm >= 1<<31 {
			pAbsTimestamp -= 1<<32 - uint64(tsn-tsm)
		} else if tsn < tsm && tsm-tsn < 1<<31 {
			pAbsTimestamp -= uint64(tsm - tsn)
		}

		if (pAbsTimestamp<<16)+uint64(p.SequenceNumber) < b.minAbsTsSeq {
			// reject this packet, it's too old.
			zap.L().Warn("rejecting packet", zap.Uint16("seq", p.SequenceNumber), zap.Uint64("absTimestamp", pAbsTimestamp), zap.Uint64("minAbsTimestamp", b.minAbsTsSeq))
			return nil
		}

		b.prevTimestamp = tsn
		b.absTimestamp = pAbsTimestamp
	}

	// add it to the buffer
	b.Lock()
	if b.buffer[p.SequenceNumber] != nil {
		// duplicate packet, but warn if timestamps are different
		if b.buffer[p.SequenceNumber].Timestamp != p.Timestamp {
			zap.L().Warn("duplicate packet with different timestamps", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("ts", p.Timestamp), zap.Uint32("prev", b.buffer[p.SequenceNumber].Timestamp))
		}
		b.Unlock()
		return nil
	}
	b.count++

	// calculate the expected emit time
	dt := time.Duration(b.absTimestamp-b.startRTPTimestamp) * time.Second / time.Duration(b.clockRate)
	emitTime := b.startNTPTimestamp.Add(dt + b.delay)

	if time.Until(emitTime) > 10*time.Second {
		zap.L().Warn("long emit time, is data being produced too fast?", zap.Uint16("seq", p.SequenceNumber), zap.Duration("dt", dt), zap.Duration("delay", b.delay))
	}

	b.buffer[p.SequenceNumber] = &BufferedPacket{
		Packet:   p,
		EmitTime: emitTime,
		AbsTsSeq: (b.absTimestamp << 16) + uint64(p.SequenceNumber),
	}
	b.Unlock()

	// broadcast the update
	b.insert.TryAcquire(1) // acquire if there is no one waiting
	b.insert.Release(1)

	return nil
}

func (b *ReorderBuffer) Len() uint16 {
	b.RLock()
	defer b.RUnlock()

	return b.count
}

func (b *ReorderBuffer) ReadRTP() (*rtp.Packet, error) {
	// ensure we have a packet to evict.
	for b.count == 0 {
		// wait for a packet to be inserted
		b.insert.Acquire(context.Background(), 1)
	}

	// if there is a packet at the eviction pointer, evict it immediately.
	b.RLock()
	if b.buffer[b.evict] != nil {
		p := b.buffer[b.evict]
		b.buffer[b.evict] = nil
		b.count--
		b.evict++
		b.minAbsTsSeq = p.AbsTsSeq + 1
		b.RUnlock()
		return p.Packet, nil
	}

	// otherwise, find the next packet and wait until its expiration time.
	lowest := time.Unix(1<<63-62135596801, 999999999) // https://stackoverflow.com/a/25065327/86433
	lowestIndex := uint16(0)

	for i, j := b.evict, b.count; j > 0; i++ {
		p := b.buffer[i]
		if p == nil {
			continue
		}
		if p.EmitTime.Before(lowest) {
			lowest = p.EmitTime
			lowestIndex = uint16(i)
		}
		j--
	}
	b.RUnlock()

	if lowest.Before(time.Now()) {
		// short circuit
		b.RLock()
		p := b.buffer[lowestIndex]
		b.buffer[lowestIndex] = nil
		b.count--
		b.evict = lowestIndex + 1
		b.minAbsTsSeq = p.AbsTsSeq + 1
		b.RUnlock()
		return p.Packet, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	update := make(chan bool)
	go func() {
		defer close(update)
		if err := b.insert.Acquire(ctx, 1); err == nil {
			update <- true
		}
	}()

	select {
	case <-update:
		// an update was received, try again
		return b.ReadRTP()

	case <-time.After(time.Until(lowest)):
		// log a warning
		if b.evict != lowestIndex {
			zap.L().Warn("lost packets", zap.Uint16("fromSeq", b.evict), zap.Uint16("toSeq", lowestIndex-1))
		}

		// then emit the next packet.
		b.RLock()
		p := b.buffer[lowestIndex]
		b.buffer[lowestIndex] = nil
		b.count--
		b.evict = lowestIndex + 1
		b.minAbsTsSeq = p.AbsTsSeq + 1
		b.RUnlock()
		return p.Packet, nil
	}
}

func (b *ReorderBuffer) Close() error {
	return nil
}

func (b *ReorderBuffer) MissingSequenceNumbers() []uint16 {
	b.RLock()
	defer b.RUnlock()

	missing := make([]uint16, 0, 1<<16)

	for i, j := b.evict, b.count; j > 0; i++ {
		if b.buffer[i] != nil {
			j--
			continue
		}
		// mark this packet as missing.
		missing = append(missing, i)
	}
	return missing
}
