package ssrc

import (
	"context"
	"encoding/binary"
	"fmt"
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

// SSRCConn is a connection that operates on the SSRC level instead of on the sender level.
type SSRCConn struct {
	sync.RWMutex

	*net.UDPConn
	sources map[webrtc.SSRC]*net.UDPAddr

	ccfb   map[string]*ccfb
	cancel context.CancelFunc
}

var udpOOBSize = func() int {
	oob4 := ipv4.NewControlMessage(ipv4.FlagDst | ipv4.FlagInterface)
	oob6 := ipv6.NewControlMessage(ipv6.FlagDst | ipv6.FlagInterface)
	if len(oob4) > len(oob6) {
		return len(oob4)
	}
	return len(oob6)
}()

// NewSSRCMux wraps a net.UDPConn and provides a way to track the SSRCs of the sender.
func NewSSRCMux(conn *net.UDPConn) (*SSRCConn, error) {
	ecn.EnableExplicitCongestionNotification(conn)

	ctx, cancel := context.WithCancel(context.Background())

	s := &SSRCConn{
		UDPConn: conn,
		sources: make(map[webrtc.SSRC]*net.UDPAddr),
		ccfb:    make(map[string]*ccfb),
		cancel:  cancel,
	}

	ccTicker := time.NewTicker(100 * time.Millisecond)
	ccSSRC := webrtc.SSRC(rand.Uint32())
	go func() {
		for {
			select {
			case <-ccTicker.C:
				s.RLock()
				for key, cc := range s.ccfb {
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
						delete(s.ccfb, key)
					}
				}
				s.RUnlock()
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, nil
}

func (s *SSRCConn) ReadFrom(p []byte) (int, net.Addr, error) {
	oob := make([]byte, udpOOBSize)
	n, oobn, _, addr, err := s.UDPConn.ReadMsgUDP(p, oob)
	if err != nil || !isRTPRTCP(p) {
		return n, addr, err
	}
	ts := time.Now()
	// this might be an RTP/RTCP packet, so try to decode the header.
	h := &rtcp.Header{}
	if err := h.Unmarshal(p); err != nil {
		// couldn't unmarshal so forward it along blindly.
		return n, addr, nil
	}

	if h.Type >= 200 && h.Type <= 207 {
		// it's an rtcp packet.
		cp, err := rtcp.Unmarshal(p)
		if err != nil {
			// not a valid rtcp packet?
			return n, addr, nil
		}
		// if it's a sender clock report, immediately respond with a receiver clock report.
		// additionally, by contract sender clocks are sent in separate packets so we don't forward.
		for _, pkt := range cp {
			switch pkt := pkt.(type) {
			case *rtcp.RawPacket:
				if pkt.Header().Type == rtcp.TypeTransportSpecificFeedback && pkt.Header().Count == 29 {
					senderClockReport := &clock.SenderClock{}
					if err := senderClockReport.Unmarshal([]byte(*pkt)[4:]); err != nil {
						log.Warn().Err(err).Msg("failed to unmarshal sender clock report")
						break
					}
					receiverClockReport := &clock.ReceiverClock{
						LastSenderNTPTime: senderClockReport.SenderNTPTime,
						Delay:             x_time.GoDurationToNTP(time.Since(ts)),
					}
					payload, err := receiverClockReport.Marshal()
					if err != nil {
						log.Warn().Err(err).Msg("failed to marshal receiver clock report")
						break
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
						log.Warn().Err(err).Msg("failed to marshal rtcp header")
						break
					}
					copy(buf, hData)
					copy(buf[len(hData):], payload)
					if _, err := s.UDPConn.WriteToUDP(buf, addr); err != nil {
						log.Error().Err(err).Msg("failed to send congestion control packet")
					}
				}
			}
		}
		return n, addr, nil
	} else {
		header := &rtp.Header{}
		if _, err := header.Unmarshal(p); err != nil {
			// not a valid rtp/rtcp packet.
			return n, addr, nil
		}
		ssrc := webrtc.SSRC(header.SSRC)
		s.Lock()
		defer s.Unlock()
		s.sources[ssrc] = addr

		// log this with congestion control.
		ecn, err := ecn.CheckExplicitCongestionNotification(oob[:oobn])
		if err != nil {
			log.Error().Err(err).Msg("failed to check ecn")
			return n, SSRCAddr(ssrc), nil
		}

		// get the twcc header sequence number.
		tccExt := &rtp.TransportCCExtension{}
		if ext := header.GetExtension(5); ext != nil {
			if err := tccExt.Unmarshal(ext); err != nil {
				log.Error().Err(err).Msg("failed to unmarshal twcc extension")
				return n, SSRCAddr(ssrc), nil
			}
		}

		fb, ok := s.ccfb[addr.String()]
		if !ok {
			fb = &ccfb{
				sender: addr,
				stream: rfc8888.NewPacketStream(),
			}
			s.ccfb[addr.String()] = fb
		}
		if err := fb.stream.AddPacket(time.Now(), ssrc, tccExt.TransportSequence, ecn); err != nil {
			log.Error().Err(err).Msg("failed to add packet to congestion control")
		}
		return n, SSRCAddr(ssrc), nil
	}
}

func (s *SSRCConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	switch addr := addr.(type) {
	case SSRCAddr:
		// this is directed at the ssrc.
		dest, ok := s.sources[webrtc.SSRC(addr)]
		if !ok {
			return 0, fmt.Errorf("no such ssrc %d", addr)
		}
		return s.UDPConn.WriteTo(p, dest)
	default:
		return s.UDPConn.WriteTo(p, addr)
	}
}

func (s *SSRCConn) Close() error {
	s.cancel()
	return s.UDPConn.Close()
}
