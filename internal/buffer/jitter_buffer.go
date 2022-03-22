package buffer

import (
	"context"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muxable/rtptools/pkg/x_range"
	"github.com/pion/rtp"
	"github.com/pion/rtpio/pkg/rtpio"
)

type JitterBuffer struct {
	sync.RWMutex

	clockRate uint32
	buffer    []*rtp.Packet
	delay     uint32

	// tail points at the last emitted packet.
	tail uint16
	// next points at the next not-null packet.
	next      uint16
	nextTimer *time.Timer
	// count is the number of packets in the buffer.
	count int32
	// emitted is true if the buffer has been emitted.
	emitted bool

	initialRTPTime        uint32
	initialRTPReceiveTime uint32

	rtpIn  rtpio.RTPReader
	rtpOut rtpio.RTPWriteCloser

	tooLateCount   uint64
	tooEarlyCount  uint64
	duplicateCount uint64

	ctx    context.Context
	cancel context.CancelFunc
}

// NewJitterBuffer creates a new singular jitter buffer with the given context and delay.
// tsClock is a clock that emits at clockRate Hz for the codec.
// delay is in RTP timestamp units.
func NewJitterBuffer(clockRate uint32, delay time.Duration, rtpIn rtpio.RTPReader) (*JitterBuffer, rtpio.RTPReader) {
	if delay == 0 {
		return &JitterBuffer{
			clockRate: clockRate,
			buffer:    make([]*rtp.Packet, 1<<16),
			delay:     0,
		}, rtpIn
	}
	r, w := rtpio.RTPPipe()
	ctx, cancel := context.WithCancel(context.Background())
	jb := &JitterBuffer{
		clockRate: clockRate,
		buffer:    make([]*rtp.Packet, 1<<16),
		delay:     uint32(delay.Seconds() * float64(clockRate)),
		nextTimer: time.NewTimer(time.Duration(math.MaxInt64)),
		rtpIn:     rtpIn,
		rtpOut:    w,
		ctx:       ctx,
		cancel:    cancel,
	}
	go jb.runTail()
	go jb.runHead()
	return jb, r
}

func (jb *JitterBuffer) rtpNow() uint32 {
	return uint32((time.Now().UnixMicro() * int64(jb.clockRate)) / 1e6)
}

func (jb *JitterBuffer) toNTPDelay(delay uint32) time.Duration {
	return time.Duration(float64(delay) * float64(time.Second) / float64(jb.clockRate))
}

func (jb *JitterBuffer) runTail() {
	for {
		select {
		case <-jb.nextTimer.C:
			jb.Lock()
			p := jb.buffer[jb.next]
			if p == nil {
				// this might happen due to an update race. it's ok.
				continue
			}
			if err := jb.rtpOut.WriteRTP(p); err != nil {
				// the output channel is closed, so we can stop writing.
				return
			}
			jb.buffer[jb.next] = nil
			jb.tail = jb.next
			jb.count--
			if jb.count > 0 {
				// find the next element and reset the timer.
				for jb.buffer[jb.next] == nil {
					jb.next++
				}
				r := jb.buffer[jb.next].Timestamp - jb.initialRTPTime
				t := jb.rtpNow() - jb.initialRTPReceiveTime
				if jb.delay+r < t {
					jb.nextTimer.Reset(0)
				} else {
					jb.nextTimer.Reset(jb.toNTPDelay(jb.delay + r - t))
				}
			} else {
				// set the timer to infinite.
				jb.nextTimer.Reset(time.Duration(math.MaxInt64))
			}
			jb.Unlock()
		case <-jb.ctx.Done():
			return
		}
	}
}

func isBefore(p *rtp.Packet, q *rtp.Packet) bool {
	if p.Timestamp == q.Timestamp {
		return p.SequenceNumber < q.SequenceNumber && (q.SequenceNumber-p.SequenceNumber) < (1<<15)
	}
	return p.Timestamp < q.Timestamp && (q.Timestamp-p.Timestamp) < (1<<31)
}

func (jb *JitterBuffer) runHead() {
	defer jb.rtpOut.Close()
	defer jb.cancel()
	for {
		p, err := jb.rtpIn.ReadRTP()
		if err != nil {
			return
		}
		now := jb.rtpNow()

		// by contract the tail is not running so these don't have to be atomic.
		if jb.initialRTPReceiveTime == 0 {
			jb.initialRTPTime = p.Timestamp
			jb.initialRTPReceiveTime = now
		}

		r := p.Timestamp - jb.initialRTPTime
		t := now - jb.initialRTPReceiveTime
		// r - t is how many ticks until the undelayed playout time.
		// delay + r - t is how many ticks until the delayed playout time.
		// if delay + r - t is negative, then the packet is too late and should be abandoned.
		// if delay + r - t is too large, then the packet is too early and the jitter buffer should be flushed.
		jb.Lock()
		if jb.delay+r < t {
			atomic.AddUint64(&jb.tooLateCount, 1)
			jb.Unlock()
			continue
		} else if jb.delay+r-t > (1 << 20) {
			// TODO: flush the jitterbuffer.
			atomic.AddUint64(&jb.tooEarlyCount, 1)
			jb.Unlock()
			continue
		} else if jb.buffer[p.SequenceNumber] != nil {
			// this is a duplicate packet.
			atomic.AddUint64(&jb.duplicateCount, 1)
			jb.Unlock()
			continue
		}
		if q := jb.buffer[jb.next]; q == nil || isBefore(p, q) {
			// this packet comes before the next emitted packet so reset the timer to emit earlier.
			if jb.nextTimer.Stop() {
				jb.next = p.SequenceNumber
				jb.nextTimer.Reset(jb.toNTPDelay(jb.delay + r - t))
				jb.buffer[p.SequenceNumber] = p
				jb.count++
			} else {
				// too late, couldn't insert this packet, so we can just drop it. this case shouldn't happen because
				// it should've been caught by the tooLateCount check above.
				log.Printf("drop race with expected delay %v", jb.delay+r-t)
			}
		} else {
			jb.buffer[p.SequenceNumber] = p
			jb.count++
		}
		jb.Unlock()
	}
}

func (jb *JitterBuffer) GetMissingSequenceNumbers(estimatedRTT uint64) []uint16 {
	jb.Lock()
	defer jb.Unlock()

	if jb.count == 0 {
		return []uint16{}
	}

	matched := make([]uint16, 0, jb.count)
	for i, p := range jb.buffer {
		if p != nil {
			matched = append(matched, uint16(i))
		}
	}

	begin, end := x_range.GetSeqRange(matched)

	missing := make([]uint16, 0, end-begin-uint16(len(matched)))
	for i := begin; i != end; i++ {
		if jb.buffer[i] == nil {
			missing = append(missing, i)
		}
	}
	return missing
}

// GetStats returns the number of packets that were too late and too early.
func (jb *JitterBuffer) GetStats() (tooLate, tooEarly uint64) {
	return atomic.LoadUint64(&jb.tooLateCount), atomic.LoadUint64(&jb.tooEarlyCount)
}

// ResetStats resets the stats.
func (jb *JitterBuffer) ResetStats() {
	atomic.StoreUint64(&jb.tooLateCount, 0)
	atomic.StoreUint64(&jb.tooEarlyCount, 0)
	atomic.StoreUint64(&jb.duplicateCount, 0)
}
