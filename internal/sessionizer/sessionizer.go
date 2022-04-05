package sessionizer

import (
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/muxable/rtptools/pkg/rfc8698/ecn"
	"github.com/muxable/rtptools/pkg/rfc8888"
	"github.com/muxable/rtptools/pkg/x_time"
	"github.com/muxable/sfu/pkg/clock"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type ccfb struct {
	stream *rfc8888.PacketStream
	sender *net.UDPAddr
}

type Session struct {
	io.Reader
	webrtc.SSRC

	w io.WriteCloser

	lastSender *net.UDPAddr
	lastRecvTs time.Time
}

type Sessionizer struct {
	sync.RWMutex

	conn     *net.UDPConn
	sessions map[webrtc.SSRC]*Session

	ccfb map[string]*ccfb // keyed by sender.

	sessionsCh chan *Session
}

var udpOOBSize = func() int {
	oob4 := ipv4.NewControlMessage(ipv4.FlagDst | ipv4.FlagInterface)
	oob6 := ipv6.NewControlMessage(ipv6.FlagDst | ipv6.FlagInterface)
	if len(oob4) > len(oob6) {
		return len(oob4)
	}
	return len(oob6)
}()

// NewSessionizer wraps a net.UDPConn and provides a way to track the SSRCs of the sender.
func NewSessionizer(conn *net.UDPConn, mtu int) (*Sessionizer, error) {
	ecn.EnableExplicitCongestionNotification(conn)

	m := &Sessionizer{
		conn:       conn,
		sessions:   make(map[webrtc.SSRC]*Session),
		ccfb:       make(map[string]*ccfb),
		sessionsCh: make(chan *Session),
	}

	ccTicker := time.NewTicker(100 * time.Millisecond)
	done := make(chan bool, 1)
	ccSSRC := webrtc.SSRC(rand.Uint32())
	go func() {
		for {
			select {
			case <-ccTicker.C:
				m.RLock()
				for key, cc := range m.ccfb {
					report := cc.stream.BuildReport(time.Now())
					payload := report.Marshal(time.Now())
					if len(payload) == 8 {
						continue
					}
					buf := make([]byte, len(payload)+8)
					header := rtcp.Header{
						Padding: false,
						Count:   11,
						Type:    rtcp.TypeTransportSpecificFeedback,
						Length:  uint16(len(payload)/4) + 1,
					}
					hData, err := header.Marshal()
					if err != nil {
						log.Error().Err(err).Msg("failed to marshal rtcp header")
						continue
					}
					binary.BigEndian.PutUint32(buf[4:8], uint32(ccSSRC))
					copy(buf[8:], payload)
					copy(buf, hData)
					if _, err := conn.WriteToUDP(buf, cc.sender); err != nil {
						log.Error().Err(err).Msg("failed to send congestion control packet")
						delete(m.ccfb, key)
					}
				}
				m.RUnlock()
			case <-done:
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, mtu)
		oob := make([]byte, udpOOBSize)
		for {
			n, oobn, _, sender, err := m.conn.ReadMsgUDP(buf, oob)
			if err != nil {
				log.Error().Err(err).Msg("failed to read udp packet")
				return
			}

			ssrcs, err := m.parseSSRCs(buf[:n], oob[:oobn], sender)
			if err != nil {
				log.Error().Err(err).Msg("failed to parse ssrcs")
				continue
			}

			m.Lock()
			for _, ssrc := range ssrcs {
				if _, ok := m.sessions[ssrc]; !ok {
					r, w := io.Pipe()
					m.sessions[ssrc] = &Session{
						Reader:     r,
						w:          w,
						lastSender: sender,
						lastRecvTs: time.Now(),
						SSRC: ssrc,
					}
					m.sessionsCh <- m.sessions[ssrc]
				}
				if _, err := m.sessions[ssrc].w.Write(buf[:n]); err != nil {
					log.Error().Err(err).Msg("failed to write to session")
				}
			}
			m.Unlock()
		}
	}()
	return m, nil
}

func (m *Sessionizer) Accept() (*Session, error) {
	s, ok := <-m.sessionsCh
	if !ok {
		return nil, io.EOF
	}
	return s, nil
}

func (m *Sessionizer) parseSSRCs(buf []byte, oob []byte, sender *net.UDPAddr) ([]webrtc.SSRC, error) {
	h := &rtcp.Header{}
	if err := h.Unmarshal(buf); err != nil {
		return nil, err
	}
	ts := time.Now()
	if h.Type >= 200 && h.Type <= 207 {
		return m.parseRTCPSSRCs(sender, buf, ts)
	} else {
		p := &rtp.Packet{}
		if err := p.Unmarshal(buf); err != nil {
			return nil, err
		}

		// log this with congestion control.
		ecn, err := ecn.CheckExplicitCongestionNotification(oob)
		if err != nil {
			return nil, err
		}

		// get the twcc header sequence number.
		tccExt := &rtp.TransportCCExtension{}
		if ext := p.Header.GetExtension(5); ext != nil {
			if err := tccExt.Unmarshal(ext); err != nil {
				return nil, err
			}
		}

		m.Lock()
		defer m.Unlock()
		fb := m.ccfb[sender.String()]
		if fb == nil {
			fb = &ccfb{
				sender: sender,
				stream: rfc8888.NewPacketStream(),
			}
			m.ccfb[sender.String()] = fb
		}
		ssrc := webrtc.SSRC(p.SSRC)
		return []webrtc.SSRC{ssrc}, fb.stream.AddPacket(time.Now(), ssrc, tccExt.TransportSequence, ecn)
	}
}

func (m *Sessionizer) parseRTCPSSRCs(sender *net.UDPAddr, buf []byte, ts time.Time) ([]webrtc.SSRC, error) {
	// it's an rtcp packet.
	cp, err := rtcp.Unmarshal(buf)
	if err != nil {
		// not a valid rtcp packet.
		return nil, err
	}
	// if it's a sender clock report, immediately respond with a receiver clock report.
	// additionally, by contract sender clocks are sent in separate packets so we don't forward.
	var ssrcs []webrtc.SSRC
	for _, p := range cp {
		switch p := p.(type) {
		case *rtcp.RawPacket:
			if p.Header().Type == rtcp.TypeTransportSpecificFeedback &&
				p.Header().Count == 29 {
				senderClockReport := &clock.SenderClock{}
				if err := senderClockReport.Unmarshal([]byte(*p)[4:]); err != nil {
					return nil, err
				}
				receiverClockReport := &clock.ReceiverClock{
					LastSenderNTPTime: senderClockReport.SenderNTPTime,
					Delay:             x_time.GoDurationToNTP(time.Since(ts)),
				}
				payload, err := receiverClockReport.Marshal()
				if err != nil {
					return nil, err
				}
				buf := make([]byte, len(payload)+4)
				header := rtcp.Header{
					Padding: false,
					Count:   30,
					Type:    rtcp.TypeTransportSpecificFeedback,
					Length:  uint16(len(payload) / 4),
				}
				hData, err := header.Marshal()
				if err != nil {
					return nil, err
				}
				copy(buf, hData)
				copy(buf[len(hData):], payload)
				if _, err := m.conn.WriteToUDP(buf, sender); err != nil {
					log.Error().Err(err).Msg("failed to send congestion control packet")
				}
				return nil, nil
			}
		}
		for _, ssrc := range p.DestinationSSRC() {
			ssrcs = append(ssrcs, webrtc.SSRC(ssrc))
		}
	}
	return ssrcs, nil
}

// Write writes to the connection sending to only senders that have sent to that ssrc.
func (m *Sessionizer) WriteRTCP(pkts []rtcp.Packet) error {
	buf, err := rtcp.Marshal(pkts)
	if err != nil {
		return err
	}
	m.RLock()
	defer m.RUnlock()
	for _, p := range pkts {
		for _, ssrc := range p.DestinationSSRC() {
			// forward this packet to that ssrc's source.
			if session, ok := m.sessions[webrtc.SSRC(ssrc)]; ok {
				if _, err := m.conn.WriteToUDP(buf, session.lastSender); err != nil {
					log.Error().Err(err).Msg("failed to send rtcp packet")
				}
			}
		}
	}
	return nil
}
