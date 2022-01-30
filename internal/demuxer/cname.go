package demuxer

import (
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

type CNAMEDemuxer struct {
	sync.Mutex

	clock func() time.Time

	bySSRC map[webrtc.SSRC]*CNAMESource
}

type CNAMESource struct {
	CNAME string

	rtpio.RTPWriteCloser
	rtpio.RTCPWriteCloser

	lastPacket time.Time
}

// NewCNAMEDemuxer creates a new CNAMEDemuxer
func NewCNAMEDemuxer(clock func() time.Time, rtpIn rtpio.RTPReader, rtcpIn rtpio.RTCPReader, onNewCNAME func(string, rtpio.RTPReader, rtpio.RTCPReader)) {
	d := &CNAMEDemuxer{clock: clock, bySSRC: make(map[webrtc.SSRC]*CNAMESource)}

	ticker := time.NewTicker(time.Second)
	done := make(chan bool, 1)

	go func() {
		for {
			p, err := rtpIn.ReadRTP()
			if err != nil {
				// close all the rtp writers.
				d.Lock()
				for _, s := range d.bySSRC {
					if err := s.RTPWriteCloser.Close(); err != nil {
						zap.L().Warn("error closing rtp writer", zap.Error(err))
					}
				}
				d.Unlock()
				return
			}

			// find the source
			d.Lock()
			s, ok := d.bySSRC[webrtc.SSRC(p.SSRC)]
			if !ok {
				zap.L().Warn("received packet for unknown ssrc", zap.Uint32("ssrc", uint32(p.SSRC)))
				d.Unlock()
				continue
			}
			// forward to this source.
			if err := s.RTPWriteCloser.WriteRTP(p); err != nil {
				zap.L().Warn("error writing rtp", zap.Error(err))
			}
			s.lastPacket = d.clock()
			d.Unlock()
		}
	}()

	go func() {
		defer ticker.Stop()
		defer func() { done <- true }()
		for {
			cp, err := rtcpIn.ReadRTCP()
			if err != nil {
				// close all the rtcp writers.
				d.Lock()
				for _, s := range d.bySSRC {
					if err := s.RTCPWriteCloser.Close(); err != nil {
						zap.L().Warn("error closing rtcp writer", zap.Error(err))
					}
				}
				d.Unlock()
				return
			}

			for _, p := range cp {
				switch p := p.(type) {
				case *rtcp.SourceDescription:
					ssrc, cname, ok := assignment(p)
					if !ok {
						break
					}

					d.Lock()
					s, ok := d.bySSRC[webrtc.SSRC(ssrc)]
					if !ok || s.CNAME != cname {
						// create a new cname source.
						rtpReader, rtpWriter := rtpio.RTPPipe()
						rtcpReader, rtcpWriter := rtpio.RTCPPipe()
						go onNewCNAME(cname, rtpReader, rtcpReader)
						zap.L().Info("new cname", zap.String("cname", cname), zap.Uint32("ssrc", uint32(ssrc)))
						d.bySSRC[webrtc.SSRC(ssrc)] = &CNAMESource{
							CNAME:           cname,
							RTPWriteCloser:  rtpWriter,
							RTCPWriteCloser: rtcpWriter,
							lastPacket:      d.clock(),
						}
					}
					d.Unlock()
				}
			}
		}
	}()

	go func() {
		defer ticker.Stop()
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

func assignment(p *rtcp.SourceDescription) (webrtc.SSRC, string, bool) {
	for _, c := range p.Chunks {
		ssrc := webrtc.SSRC(c.Source)
		for _, item := range c.Items {
			if item.Type == rtcp.SDESCNAME {
				cname := item.Text
				return ssrc, cname, true
			}
		}
	}
	return 0, "", false
}

// source finds the source matching a cname and ssrc pair.
func (d *CNAMEDemuxer) byCNAME(cname string) (*CNAMESource, bool) {
	for _, s := range d.bySSRC {
		if s.CNAME == cname {
			return s, true
		}
	}
	return nil, false
}

// cleanup removes any cname sources that haven't received a packet in the last 30 seconds
func (d *CNAMEDemuxer) cleanup() {
	d.Lock()
	defer d.Unlock()

	now := d.clock()
	for ssrc, s := range d.bySSRC {
		if now.Sub(s.lastPacket) > 30*time.Second {
			// log the removal
			zap.L().Info("removing cname source", zap.String("cname", s.CNAME), zap.Uint32("ssrc", uint32(ssrc)))
			delete(d.bySSRC, ssrc)
			// close the output channels
			s.RTPWriteCloser.Close()
			s.RTCPWriteCloser.Close()
		}
	}
}
