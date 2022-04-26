package buffer

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"go.uber.org/zap"
)

type BufferedPacket struct {
	*rtp.Packet
	EvictRTPTime uint64
	AbsTsSeq     uint64
	Evicted      bool // tombstone packets to allow late-arriving packets to be discarded
}

type ReorderBuffer struct {
	sync.Mutex

	clockRate         uint32
	delay             uint64
	buffer            [1 << 16]*BufferedPacket
	prevTimestamp     uint32
	absTimestamp      uint64
	minAbsTsSeq       uint64
	startRTPTimestamp uint64
	startNTPTimestamp time.Time
	count             uint16

	evict uint16

	insert  chan struct{}
	tickers []*time.Ticker
}

// NewJitterBuffer creates a new singular jitter buffer with the given context && delay.
// tsClock is a clock that emits at clockRate Hz for the codec.
// delay is in RTP timestamp units.
func NewReorderBuffer(clockRate uint32, delay time.Duration) *ReorderBuffer {
	return &ReorderBuffer{
		clockRate: clockRate,
		delay:     uint64(delay.Seconds() * float64(clockRate)),
	}
}

func (b *ReorderBuffer) WriteRTP(p *rtp.Packet) error {
	b.Lock()
	defer b.Unlock()

	// update the timestamp counters
	if b.absTimestamp == 0 {
		b.prevTimestamp = p.Timestamp
		b.absTimestamp = uint64(p.Timestamp)
		b.startRTPTimestamp = uint64(p.Timestamp)
		b.startNTPTimestamp = time.Now()
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
			if q := b.buffer[p.SequenceNumber]; q == nil || q.Timestamp != p.Timestamp {
				zap.L().Warn("rejecting packet",
					zap.Uint16("seq", p.SequenceNumber),
					zap.Uint32("ts", p.Timestamp),
					zap.Uint64("absTimestamp", pAbsTimestamp),
					zap.Uint64("minAbsTimestamp", b.minAbsTsSeq),
					zap.Uint16("evict", b.evict))
			}
			return nil
		}

		b.prevTimestamp = tsn
		b.absTimestamp = pAbsTimestamp
	}

	// calculate the expected emit time
	// dt := time.Duration(b.absTimestamp-b.startRTPTimestamp) * time.Second / time.Duration(b.clockRate)
	// emitTime := b.startNTPTimestamp.Add(b.absTimestamp + b.delay)

	zap.L().Debug("received packet", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("timestamp", p.Timestamp), zap.Uint16("evict", b.evict), zap.Uint16("count", b.count), zap.Uint64("absTimestamp", b.absTimestamp))

	// add it to the buffer
	if b.buffer[p.SequenceNumber] != nil {
		// duplicate packet, but warn if timestamps are different
		if b.buffer[p.SequenceNumber].Timestamp != p.Timestamp {
			zap.L().Warn("duplicate packet with different timestamps", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("ts", p.Timestamp), zap.Uint32("prev", b.buffer[p.SequenceNumber].Timestamp))
		}
		return nil
	}
	b.count++

	// if time.Until(emitTime) > 10*time.Second {
	// 	zap.L().Warn("long emit time, is data being produced too fast?", zap.Uint16("seq", p.SequenceNumber), zap.Duration("dt", dt), zap.Duration("delay", b.delay))
	// }

	b.buffer[p.SequenceNumber] = &BufferedPacket{
		Packet:       p,
		EvictRTPTime: b.absTimestamp + b.delay,
		AbsTsSeq:     (b.absTimestamp << 16) + uint64(p.SequenceNumber),
	}

	// notify the reader if it's waiting
	if b.insert != nil {
		close(b.insert)
		b.insert = nil
	}

	return nil
}

func (b *ReorderBuffer) Len() uint16 {
	b.Lock()
	defer b.Unlock()

	return b.count
}

func (b *ReorderBuffer) ReadRTP() (*rtp.Packet, error) {
	for { // loop until we find a packet
		// ensure we have a packet to evict.
		b.Lock()
		for b.count == 0 {
			// this is some real fancy footwork.
			ch := make(chan struct{})
			b.insert = ch
			b.Unlock()
			<-ch
			b.Lock()
		}

		// if there is a packet at the eviction pointer, evict it immediately.
		if b.buffer[b.evict] != nil {
			p := b.buffer[b.evict]
			b.buffer[b.evict].Evicted = true
			b.count--
			b.evict++
			b.minAbsTsSeq = p.AbsTsSeq + 1
			b.Unlock()
			zap.L().Debug("immediate read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}

		// otherwise, find the next packet and wait until its expiration time.
		lowest := uint64(math.MaxUint64)
		lowestIndex := uint16(0)

		for i, j := b.evict, b.count; j > 0; i++ {
			p := b.buffer[i]
			if p == nil || p.Evicted {
				continue
			}
			if p.EvictRTPTime < lowest {
				lowest = p.EvictRTPTime
				lowestIndex = uint16(i)
			}
			j--
		}

		rtpnow := b.rtpNow()

		if lowest < rtpnow {
			// short circuit
			p := b.buffer[lowestIndex]
			b.buffer[lowestIndex].Evicted = true
			b.count--
			b.evict = lowestIndex + 1
			b.minAbsTsSeq = p.AbsTsSeq + 1
			b.Unlock()
			zap.L().Debug("short circuited read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}

		dt := time.Duration(lowest-rtpnow) * time.Second / time.Duration(b.clockRate)
		// contest with an update.
		ch := make(chan struct{})
		b.insert = ch
		b.Unlock()
		select {
		case <-ch:
			// an update was received, try again
			break

		case <-time.After(dt):
			// log a warning
			b.Lock()
			if b.evict != lowestIndex {
				zap.L().Warn("lost packets", zap.Uint16("fromSeq", b.evict), zap.Uint16("toSeq", lowestIndex-1), zap.Duration("dt", dt))
			}

			// then emit the next packet.
			p := b.buffer[lowestIndex]
			b.buffer[lowestIndex].Evicted = true
			b.count--
			b.evict = lowestIndex + 1
			b.minAbsTsSeq = p.AbsTsSeq + 1
			b.Unlock()
			zap.L().Debug("delayed read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}
	}
}

func (b *ReorderBuffer) Close() error {
	b.Lock()
	defer b.Unlock()

	close(b.insert)

	// close any nack tickers
	for _, n := range b.tickers {
		n.Stop()
	}

	return nil
}

func (b *ReorderBuffer) MissingSequenceNumbers() []uint16 {
	b.Lock()
	defer b.Unlock()

	missing := make([]uint16, 0, 1<<16)

	for i, j := b.evict, b.count; j > 0; i++ {
		if p := b.buffer[i]; p != nil && !p.Evicted {
			j--
			continue
		}
		// mark this packet as missing.
		missing = append(missing, i)
	}
	return missing
}

func (b *ReorderBuffer) rtpNow() uint64 {
	now := time.Now()
	ntpdt := now.Sub(b.startNTPTimestamp)
	return b.startRTPTimestamp + uint64(ntpdt.Seconds()*float64(b.clockRate))
}

func (b *ReorderBuffer) Nacks(interval time.Duration) <-chan *rtcp.TransportLayerNack {
	ticker := time.NewTicker(interval)
	b.tickers = append(b.tickers, ticker)
	ch := make(chan *rtcp.TransportLayerNack)
	senderSSRC := rand.Uint32()
	go func() {
		for range ticker.C {
			missing := b.MissingSequenceNumbers()
			if len(missing) > 0 {
				nack := &rtcp.TransportLayerNack{
					SenderSSRC: senderSSRC,
					Nacks:      rtcp.NackPairsFromSequenceNumbers(missing),
				}
				ch <- nack
			}
		}
	}()
	return ch
}
