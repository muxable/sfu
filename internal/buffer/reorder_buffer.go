package buffer

import (
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"go.uber.org/zap"
)

type BufferedPacket struct {
	*rtp.Packet
	EvictRTPTime time.Time
	Evicted      bool // tombstone packets to allow late-arriving packets to be discarded
}

type ReorderBuffer struct {
	sync.Mutex

	clockRate         uint32
	delay             time.Duration
	buffer            [1 << 16]*BufferedPacket
	prevTimestamp     uint32
	absTimestamp      uint64
	startRTPTimestamp uint64
	startNTPTimestamp time.Time
	count             uint16

	evict uint16

	insert  chan struct{}
	tickers []*time.Ticker
	writeCh chan *rtp.Packet
}

// NewJitterBuffer creates a new singular jitter buffer with the given context && delay.
// tsClock is a clock that emits at clockRate Hz for the codec.
// delay is in RTP timestamp units.
func NewReorderBuffer(clockRate uint32, delay time.Duration) *ReorderBuffer {
	b := &ReorderBuffer{
		clockRate: clockRate,
		delay:     delay,
		writeCh:   make(chan *rtp.Packet, 16),
	}
	go b.writeLoop()
	return b
}

func (b *ReorderBuffer) WriteRTP(p *rtp.Packet) error {
	log.Printf("write %v", p.SequenceNumber)
	b.writeCh <- p
	return nil
}

func (b *ReorderBuffer) writeLoop() {
	for p := range b.writeCh {
		b.Lock()

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

			// if (pAbsTimestamp<<16)+uint64(p.SequenceNumber) < b.minAbsTsSeq {
			// 	// reject this packet, it's too old.
			// 	if q := b.buffer[p.SequenceNumber]; q == nil || q.Timestamp != p.Timestamp {
			// 		zap.L().Warn("rejecting packet",
			// 			zap.Uint16("seq", p.SequenceNumber),
			// 			zap.Uint32("ts", p.Timestamp),
			// 			zap.Uint64("absTimestamp", (pAbsTimestamp<<16)+uint64(p.SequenceNumber)),
			// 			zap.Uint64("minAbsTimestamp", b.minAbsTsSeq),
			// 			zap.Uint16("evict", b.evict))
			// 	}
			// 	b.Unlock()
			// 	continue
			// }

			b.prevTimestamp = tsn
			b.absTimestamp = pAbsTimestamp
		}

		// calculate the expected emit time
		// dt := time.Duration(b.absTimestamp-b.startRTPTimestamp) * time.Second / time.Duration(b.clockRate)
		// emitTime := b.startNTPTimestamp.Add(b.absTimestamp + b.delay)

		// zap.L().Debug("received packet", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("timestamp", p.Timestamp), zap.Uint16("evict", b.evict), zap.Uint16("count", b.count), zap.Uint64("absTimestamp", b.absTimestamp))

		// add it to the buffer
		if q := b.buffer[p.SequenceNumber]; q != nil && !q.Evicted {
			// duplicate packet, but warn if timestamps are different
			if q.Timestamp != p.Timestamp {
				zap.L().Warn("duplicate packet with different timestamps", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("ts", p.Timestamp), zap.Uint32("prev", q.Timestamp))
			}
			b.Unlock()
			continue
		}
		b.count++

		// if time.Until(emitTime) > 10*time.Second {
		// 	zap.L().Warn("long emit time, is data being produced too fast?", zap.Uint16("seq", p.SequenceNumber), zap.Duration("dt", dt), zap.Duration("delay", b.delay))
		// }

		b.buffer[p.SequenceNumber] = &BufferedPacket{
			Packet:       p,
			EvictRTPTime: time.Now().Add(b.delay),
		}

		// notify the reader if it's waiting
		if b.insert != nil {
			close(b.insert)
			b.insert = nil
		}

		b.Unlock()
	}
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
		if p := b.buffer[b.evict]; p != nil {
			p.Evicted = true
			b.count--
			b.evict++
			b.Unlock()
			log.Printf("immediate read %v %v", p.Packet.SequenceNumber, b.count)
			// zap.L().Debug("immediate read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}

		// otherwise, find the next packet and wait until its expiration time.
		var p *BufferedPacket
		i := b.evict
		for ; p == nil || p.Evicted; i++ {
			p = b.buffer[i]
		}

		// contest with an update.
		ch := make(chan struct{})
		b.insert = ch
		b.Unlock()
		select {
		case <-ch:
			// an update was received, try again
			break

		case <-time.After(b.delay):
			// log a warning
			b.Lock()
			if i != b.evict {
				zap.L().Warn("lost packets", zap.Uint16("fromSeq", b.evict), zap.Uint16("toSeq", i-1))
			}

			// then emit the next packet.
			p.Evicted = true
			b.count--
			b.evict = i + 1
			b.Unlock()
			zap.L().Debug("delayed read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}
	}
}

func (b *ReorderBuffer) Close() error {
	b.Lock()
	defer b.Unlock()

	close(b.writeCh)
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

	// for i, j := b.evict, b.count; j > 0; i++ {
	// 	if p := b.buffer[i]; p != nil && !p.Evicted {
	// 		j--
	// 		continue
	// 	}
	// 	// mark this packet as missing, ignoring the [0, b.evict) range if uninitialized.
	// 	if b.evict > 0 || j < b.count {
	// 		missing = append(missing, i)
	// 	}
	// }
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
