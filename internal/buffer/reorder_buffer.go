package buffer

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/pion/rtp"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
)

type ReorderBuffer struct {
	sync.RWMutex

	clockRate         uint32
	delay             time.Duration
	buffer            [1 << 16]*rtp.Packet
	emitTime          [1 << 16]time.Time
	prevTimestamp     uint32
	absTimestamp      uint64
	startRTPTimestamp uint64
	startNTPTimestamp time.Time
	count             uint16

	evict  uint16
	insert *semaphore.Weighted

	// whether to hold packets until the deadline or early-emit them.
	hold bool
}

// NewJitterBuffer creates a new singular jitter buffer with the given context && delay.
// tsClock is a clock that emits at clockRate Hz for the codec.
// delay is in RTP timestamp units.
func NewReorderBuffer(clockRate uint32, delay time.Duration, hold bool) *ReorderBuffer {
	return &ReorderBuffer{
		clockRate: clockRate,
		delay:     delay,
		insert:    semaphore.NewWeighted(1),
		hold:      hold,
	}
}

func (b *ReorderBuffer) WriteRTP(p *rtp.Packet) error {
	if b.prevTimestamp == 0 {
		// initialize
		b.prevTimestamp = p.Timestamp
		b.absTimestamp = uint64(p.Timestamp)
		b.startRTPTimestamp = uint64(p.Timestamp)
		b.startNTPTimestamp = time.Now()
	} else {
		// calculate the true timestamp
		tsn := p.Timestamp
		tsm := b.prevTimestamp

		if tsn > tsm && tsn-tsm < 1<<31 {
			b.absTimestamp += uint64(tsn - tsm)
		} else if tsn < tsm && tsm-tsn >= 1<<31 {
			b.absTimestamp += 1<<32 - uint64(tsm-tsn)
		} else if tsn > tsm && tsn-tsm >= 1<<31 {
			b.absTimestamp -= 1<<32 - uint64(tsn-tsm)
		} else if tsn < tsm && tsm-tsn < 1<<31 {
			b.absTimestamp -= uint64(tsm - tsn)
		}

		b.prevTimestamp = tsn
	}

	// add it to the buffer
	b.Lock()
	if b.buffer[p.SequenceNumber] == nil {
		if b.count == 0 {
			// this is the first element, set the eviction pointer.
			b.evict = p.SequenceNumber
		}
		b.count++
	}
	b.buffer[p.SequenceNumber] = p
	b.Unlock()

	// calculate the expected emit time
	dt := time.Duration(b.absTimestamp-b.startRTPTimestamp) * time.Second / time.Duration(b.clockRate)
	b.emitTime[p.SequenceNumber] = b.startNTPTimestamp.Add(dt + b.delay)

	if time.Until(b.emitTime[p.SequenceNumber]) > 10*time.Second {
		zap.L().Warn("long emit time, is data being produced too fast?", zap.Uint16("seq", p.SequenceNumber), zap.Duration("dt", dt), zap.Duration("delay", b.delay))
	}

	// broadcast the update
	b.insert.TryAcquire(1) // acquire if there is no one waiting
	b.insert.Release(1)

	return nil
}

func (b *ReorderBuffer) ReadRTP() (*rtp.Packet, error) {
	// ensure we have a packet to evict.
	for b.count == 0 {
		// wait for a packet to be inserted
		b.insert.Acquire(context.Background(), 1)
	}

	// if there is a packet at the eviction pointer, evict it immediately.
	b.RLock()
	if !b.hold && b.buffer[b.evict] != nil {
		p := b.buffer[b.evict]
		b.buffer[b.evict] = nil
		b.count--
		b.evict++
		b.RUnlock()
		return p, nil
	}

	// otherwise, find the next packet and wait until its expiration time.
	lowest := time.Duration(math.MaxInt64)
	lowestIndex := uint16(0)

	for i, p := range b.buffer {
		if p == nil {
			continue
		}
		dt := time.Until(b.emitTime[i])
		if dt < lowest {
			lowest = dt
			lowestIndex = uint16(i)
		}
	}
	b.RUnlock()

	if lowest < 0 {
		// short circuit
		b.RLock()
		p := b.buffer[lowestIndex]
		b.buffer[lowestIndex] = nil
		b.count--
		b.evict = lowestIndex + 1
		b.RUnlock()
		return p, nil
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

	case <-time.After(lowest):
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
		b.RUnlock()
		return p, nil
	}
}

func (b *ReorderBuffer) Close() error {
	return nil
}

func (b *ReorderBuffer) GetMissingSequenceNumbers(estimatedRTT uint64) []uint16 {
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
