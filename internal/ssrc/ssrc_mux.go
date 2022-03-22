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
	"go.uber.org/zap"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type ccfb struct {
	stream *rfc8888.PacketStream
	sender *net.UDPAddr
}

// SSRCMux is a connection that operates on the SSRC level instead of on the sender level.
type SSRCMux struct {
	sync.RWMutex

	*net.UDPConn

	bondedAddrs map[string]*net.UDPAddr

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
func NewSSRCMux(conn *net.UDPConn) (*SSRCMux, error) {
	ecn.EnableExplicitCongestionNotification(conn)

	ctx, cancel := context.WithCancel(context.Background())

	s := &SSRCMux{
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

func (s *SSRCMux) masqueradedAddr(addr *net.UDPAddr) *net.UDPAddr {
	bonded, ok := s.bondedAddrs[addr.String()]
	if !ok {
		return addr
	}
	return bonded
}

func (s *SSRCMux) ReadFrom(p []byte) (int, net.Addr, error) {
	oob := make([]byte, udpOOBSize)
	n, oobn, _, addr, err := s.UDPConn.ReadMsgUDP(p, oob)
	if err != nil {
		return n, s.masqueradedAddr(addr), err
	}
	ts := time.Now()
	// check if it's one of our bonding request packets.
	r := &BondingRequest{}
	if err := r.Unmarshal(p[:n]); err != nil {
		return n, s.masqueradedAddr(addr), err
	}
	// this is a bonding request, so we need to remap this address to the requested key.
	s.bondedAddrs[addr.String()] = r.KeyAddr()
	// then respond with a bonding response.
	response := &BondingResponse{
		ntp:   r.ntp,
		delay: x_time.GoDurationToNTP(time.Since(ts)),
	}
	payload, err := response.Marshal()
	if err != nil {
		zap.L().Error("failed to marshal bonding response", zap.Error(err))
		return n, s.masqueradedAddr(addr), err
	}
	if _, err := s.UDPConn.WriteToUDP(payload, addr); err != nil {
		zap.L().Error("failed to send bonding response", zap.Error(err))
	}

	return n, s.masqueradedAddr(addr), err

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
		s.Lock()
		defer s.Unlock()
		s.sources[webrtc.SSRC(header.SSRC)] = addr
		ip := []byte{0, 0, 0, 0}
		binary.BigEndian.PutUint32(ip, header.SSRC)

		// log this with congestion control.
		ecn, err := ecn.CheckExplicitCongestionNotification(oob[:oobn])
		if err != nil {
			log.Error().Err(err).Msg("failed to check ecn")
			return n, addr, nil
		}

		// get the twcc header sequence number.
		tccExt := &rtp.TransportCCExtension{}
		if ext := header.GetExtension(5); ext != nil {
			if err := tccExt.Unmarshal(ext); err != nil {
				log.Error().Err(err).Msg("failed to unmarshal twcc extension")
				return n, addr, nil
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
		if err := fb.stream.AddPacket(time.Now(), webrtc.SSRC(header.SSRC), tccExt.TransportSequence, ecn); err != nil {
			log.Error().Err(err).Msg("failed to add packet to congestion control")
		}
		return n, addr, nil
	}
}

func (s *SSRCMux) WriteTo(p []byte, addr net.Addr) (int, error) {
	switch addr := addr.(type) {
	case *net.UDPAddr:
		if addr.Port == 0 {
			ssrc := webrtc.SSRC(binary.BigEndian.Uint32(addr.IP))
			// this is directed at the ssrc.
			dest, ok := s.sources[ssrc]
			if !ok {
				return 0, fmt.Errorf("no such ssrc %v", addr)
			}
			return s.UDPConn.WriteTo(p, dest)
		}
	}
	return s.UDPConn.WriteTo(p, addr)
}

func (s *SSRCMux) Close() error {
	s.cancel()
	return s.UDPConn.Close()
}

var _ net.PacketConn = (*SSRCMux)(nil)
