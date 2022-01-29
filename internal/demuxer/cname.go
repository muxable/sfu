package demuxer

import (
	"sync"
	"time"

	"github.com/muxable/rtpio/pkg/rtpio"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

type CNAMEDemuxer struct {
	sync.Mutex

	clock func() time.Time

	onNewCNAME func(string, rtpio.RTPReader, rtpio.RTCPReader)

	bySSRC map[webrtc.SSRC]*CNAMESource
}

type CNAMESource struct {
	CNAME string

	rtpio.RTPWriteCloser
	rtpio.RTCPWriteCloser

	lastPacket time.Time
	closed     bool
}

// NewCNAMEDemuxer creates a new CNAMEDemuxer
func NewCNAMEDemuxer(clock func() time.Time, rtpIn rtpio.RTPReader, rtcpIn rtpio.RTCPReader, onNewCNAME func(string, rtpio.RTPReader, rtpio.RTCPReader)) {
	d := &CNAMEDemuxer{clock: clock}

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
					// check if there's already a cname source created for this cname.
					source, ok := d.byCNAME(cname)
					if !ok {
						// create a new cname source.
						rtpReader, rtpWriter := rtpio.RTPPipe()
						rtcpReader, rtcpWriter := rtpio.RTCPPipe()
						source = &CNAMESource{
							CNAME:           cname,
							RTPWriteCloser:  rtpWriter,
							RTCPWriteCloser: rtcpWriter,
							lastPacket:      d.clock(),
						}
						go d.onNewCNAME(cname, rtpReader, rtcpReader)

						zap.L().Info("new cname", zap.String("cname", cname), zap.Uint32("ssrc", uint32(ssrc)))
					}

					// assign the source to the ssrc.
					d.bySSRC[webrtc.SSRC(ssrc)] = source
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
		if now.Sub(s.lastPacket) > 30*time.Second || s.closed {
			// log the removal
			zap.L().Info("removing cname source", zap.String("cname", s.CNAME), zap.Uint32("ssrc", uint32(ssrc)))
			delete(d.bySSRC, ssrc)
			if !s.closed {
				// close the output channels
				s.RTPWriteCloser.Close()
				s.RTCPWriteCloser.Close()
			}
		}
	}
}
