package buffer

import (
	"io"
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
	AbsTimestamp uint64
}

type ReorderBuffer struct {
	sync.Mutex

	clockRate uint32
	delay     uint64
	buffer    [1 << 16]*BufferedPacket

	ladder *packetLadder32
	memory shortTermMemory

	count  uint16
	evict  uint16
	closed bool

	insert  chan struct{}
	tickers []*time.Ticker
}

// NewJitterBuffer creates a new singular jitter buffer with the given context && delay.
// tsClock is a clock that emits at clockRate Hz for the codec.
// delay is in RTP timestamp units.
func NewReorderBuffer(clockRate uint32, delay time.Duration) *ReorderBuffer {
	return &ReorderBuffer{
		ladder:    &packetLadder32{},
		clockRate: clockRate,
		delay:     uint64(delay.Seconds() * float64(clockRate)),
	}
}

func (b *ReorderBuffer) WriteRTP(p *rtp.Packet) error {
	b.Lock()
	defer b.Unlock()

	// get the true timestamp
	ts := b.ladder.AbsTimestamp(p)

	// if the timestamp is earlier than the ladder's reference timestamp, then the packet is too late. discard it.
	if ts < b.ladder.RefTimestamp || (ts == b.ladder.RefTimestamp && (p.SequenceNumber < b.evict || (p.SequenceNumber-b.evict) > (1<<15))) {
		// check the short term memory to see if we recently emitted this packet. if so, then it's a duplicate. if not, then it's too late.
		if !b.memory.Contains(p) {
			zap.L().Warn("discarding late packet", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("ts", p.Timestamp))
		} else {
			// TODO: increase the nack offset.
		}
		return nil
	}

	// if the packet already exists, then ignore it.
	if q := b.buffer[p.SequenceNumber]; q != nil {
		if q.Timestamp != p.Timestamp {
			zap.L().Warn("duplicate packet with different timestamps", zap.Uint16("seq", p.SequenceNumber), zap.Uint32("ts", p.Timestamp), zap.Uint32("prev", q.Timestamp))
		} else {
			// TODO: increase the nack offset.
		}
		return nil
	}
	b.count++

	b.buffer[p.SequenceNumber] = &BufferedPacket{
		AbsTimestamp: ts,
		Packet:       p,
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
		for b.count == 0 && !b.closed {
			// this is some real fancy footwork.
			ch := make(chan struct{})
			b.insert = ch
			b.Unlock()
			<-ch
			b.Lock()
		}
		if b.closed {
			b.Unlock()
			return nil, io.EOF
		}

		// if there is a packet at the eviction pointer, evict it immediately.
		if b.buffer[b.evict] != nil {
			p := b.buffer[b.evict]
			log.Printf("got packet %v", p)
			b.ladder.RefTimestamp = p.AbsTimestamp
			b.buffer[b.evict] = nil
			b.count--
			b.evict++
			b.Unlock()
			// zap.L().Debug("immediate read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}

		// otherwise, find the next packet and wait until its expiration time.
		i := b.evict
		for ; b.buffer[i] == nil; i++ {
		}

		rtpnow := b.ladder.RefTimestamp

		dt := time.Duration(b.buffer[i].AbsTimestamp-rtpnow+b.delay) * time.Second / time.Duration(b.clockRate)
		// contest with an update.
		ch := make(chan struct{})
		b.insert = ch
		b.Unlock()
		select {
		case <-ch:
			// an update was received, try again

		case <-time.After(dt):
			// log a warning
			b.Lock()
			if b.evict != i {
				zap.L().Warn("lost packets", zap.Uint16("fromSeq", b.evict), zap.Uint16("toSeq", i-1), zap.Duration("dt", dt))
			}

			// then emit the next packet.
			p := b.buffer[i]
			b.buffer[i] = nil
			b.count--
			b.evict = i + 1
			b.ladder.RefTimestamp = p.AbsTimestamp
			b.Unlock()
			zap.L().Debug("delayed read", zap.Uint16("seq", p.Packet.SequenceNumber), zap.Uint32("ts", p.Packet.Timestamp))
			return p.Packet, nil
		}
	}
}

func (b *ReorderBuffer) Close() error {
	b.Lock()
	defer b.Unlock()

	b.closed = true

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
		if p := b.buffer[i]; p != nil {
			j--
			continue
		}
		// mark this packet as missing, ignoring the [0, b.evict) range if uninitialized.
		if b.ladder.RefTimestamp > 0 || j < b.count {
			missing = append(missing, i)
		}
	}
	return missing
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
