package analyzer

import (
	"io"
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
)

type Results struct {
	CNAME       string
	PayloadType webrtc.PayloadType
}

type Analyzer struct {
	cname string
	pt    webrtc.PayloadType

	signal sync.Cond

	rtpCh  chan *rtp.Packet
	rtcpCh chan []rtcp.Packet
}

func NewAnalyzer(size int) (*Analyzer, error) {
	return &Analyzer{
		signal: sync.Cond{L: &sync.Mutex{}},
		rtpCh:  make(chan *rtp.Packet, size),
		rtcpCh: make(chan []rtcp.Packet, size),
	}, nil
}

func (a *Analyzer) ReadResults() *Results {
	a.signal.L.Lock()
	for a.cname == "" || a.pt == webrtc.PayloadType(0) {
		a.signal.Wait()
	}
	a.signal.L.Unlock()

	return &Results{
		CNAME:       a.cname,
		PayloadType: a.pt,
	}
}

func (a *Analyzer) WriteRTP(p *rtp.Packet) error {
	a.signal.L.Lock()
	a.pt = webrtc.PayloadType(p.PayloadType)
	if len(a.rtpCh) == cap(a.rtpCh) {
		// set the cname to default.
		a.cname = "mugit"
	}
	a.signal.Broadcast()
	a.signal.L.Unlock()
	a.rtpCh <- p
	return nil
}

func (a *Analyzer) WriteRTCP(p []rtcp.Packet) error {
	for _, pkt := range p {
		switch pkt := pkt.(type) {
		case *rtcp.SourceDescription:
			for _, c := range pkt.Chunks {
				for _, item := range c.Items {
					if item.Type == rtcp.SDESCNAME {
						a.signal.L.Lock()
						a.cname = item.Text
						a.signal.Broadcast()
						a.signal.L.Unlock()
					}
				}
			}
		}
	}
	if len(a.rtcpCh) == cap(a.rtcpCh) {
		// this is almost certainly an error, we don't want to block here.
		return io.ErrShortWrite
	}
	a.rtcpCh <- p
	return nil
}

func (a *Analyzer) Write(buf []byte) (int, error) {
	h := &rtcp.Header{}
	if err := h.Unmarshal(buf); err != nil {
		return 0, err
	}
	if h.Type >= 200 && h.Type <= 207 {
		p, err := rtcp.Unmarshal(buf)
		if err != nil {
			return 0, err
		}
		return len(buf), a.WriteRTCP(p)
	} else {
		p := &rtp.Packet{}
		if err := p.Unmarshal(buf); err != nil {
			return 0, err
		}
		return len(buf), a.WriteRTP(p)
	}
}

func (a *Analyzer) ReadRTP() (*rtp.Packet, error) {
	if p, ok := <-a.rtpCh; ok {
		return p, nil
	}
	return nil, io.EOF
}

func (a *Analyzer) ReadRTCP() ([]rtcp.Packet, error) {
	if p, ok := <-a.rtcpCh; ok {
		return p, nil
	}
	return nil, io.EOF
}

func (a *Analyzer) Close() error {
	close(a.rtpCh)
	close(a.rtcpCh)
	return nil
}

var _ rtpio.RTPReader = (*Analyzer)(nil)
var _ rtpio.RTCPReader = (*Analyzer)(nil)
var _ rtpio.RTPWriter = (*Analyzer)(nil)
var _ rtpio.RTCPWriter = (*Analyzer)(nil)
var _ io.Closer = (*Analyzer)(nil)
