package demuxer

import (
	"time"

	"github.com/muxable/rtpio/pkg/rtpio"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

type PayloadTypeSource struct {
	rtpWriter  rtpio.RTPWriteCloser
	lastPacket time.Time
}

type PayloadTypeDemuxer struct {
	clock         func() time.Time
	byPayloadType map[webrtc.PayloadType]*PayloadTypeSource
}

// NewPayloadTypeDemuxer creates a new PayloadTypeDemuxer
func NewPayloadTypeDemuxer(clock func() time.Time, rtpIn rtpio.RTPReader, onNewPayloadType func(webrtc.PayloadType, rtpio.RTPReader)) {
	d := &PayloadTypeDemuxer{
		clock:         clock,
		byPayloadType: make(map[webrtc.PayloadType]*PayloadTypeSource),
	}

	ticker := time.NewTicker(time.Second)
	done := make(chan bool, 1)

	go func() {
		defer ticker.Stop()
		defer func() { done <- true }()
		for {
			p := &rtp.Packet{}
			if _, err := rtpIn.ReadRTP(p); err != nil {
				// close all the rtp writers.
				for _, s := range d.byPayloadType {
					s.rtpWriter.Close()
				}
				return
			}

			pt := webrtc.PayloadType(p.PayloadType)
			s, ok := d.byPayloadType[pt]
			if !ok {
				r, w := rtpio.RTPPipe()
				s = &PayloadTypeSource{rtpWriter: w}
				d.byPayloadType[pt] = s
				go onNewPayloadType(pt, r)
			}
			s.lastPacket = d.clock()
			s.rtpWriter.WriteRTP(p)
		}
	}()

	go func() {
		for {
			select {
			case <-ticker.C:
				d.cleanup()
			case <-done:
				return
			}
		}
	}()
}

// cleanup removes any payload types that have been inactive for a while.
func (d *PayloadTypeDemuxer) cleanup() {
	now := d.clock()
	for pt, s := range d.byPayloadType {
		if now.Sub(s.lastPacket) > 30*time.Second {
			// log the removal
			delete(d.byPayloadType, pt)
			// close the output channels
			s.rtpWriter.Close()
		}
	}
}
