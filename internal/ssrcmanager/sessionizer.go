package sessionizer

import (
	"encoding/binary"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/muxable/ingress/pkg/clock"
	"github.com/muxable/rtpio/pkg/rtpio"
	"github.com/muxable/rtptools/pkg/rfc8698"
	"github.com/muxable/rtptools/pkg/rfc8888"
	"github.com/muxable/rtptools/pkg/x_time"
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

type SSRCManager struct {
	sync.RWMutex
	rtpio.RTCPWriter

	conn    *net.UDPConn
	sources map[webrtc.SSRC]*net.UDPAddr

	ccfb map[string]*ccfb

	rtcpWriter rtpio.RTCPWriter
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
func NewSessionizer(conn *net.UDPConn, mtu int) (rtpio.RTPReader, rtpio.RTCPReader, rtpio.RTCPWriter) {
	rfc8698.EnableExplicitCongestionNotification(conn)

	rtpReader, rtpWriter := rtpio.RTPPipe()
	rtcpReader, rtcpWriter := rtpio.RTCPPipe()

	m := &SSRCManager{
		conn:       conn,
		sources:    make(map[webrtc.SSRC]*net.UDPAddr),
		ccfb:       make(map[string]*ccfb),
		rtcpWriter: rtcpWriter,
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
		defer func() { done <- true }()
		for {
			n, oobn, _, sender, err := m.conn.ReadMsgUDP(buf, oob)
			if err != nil {
				return
			}
			ts := time.Now()
			h := &rtcp.Header{}
			if err := h.Unmarshal(buf[:n]); err != nil {
				// not a valid rtp/rtcp packet.
				continue
			}
			if h.Type >= 200 && h.Type <= 207 {
				if err := m.handleRTCP(sender, buf[:n], ts); err != nil {
					log.Error().Err(err).Msg("failed to handle rtcp packet")
				}
			} else {
				p := &rtp.Packet{}
				if err := p.Unmarshal(buf[:n]); err != nil {
					// not a valid rtp/rtcp packet.
					continue
				}
				if _, err := rtpWriter.WriteRTP(p); err != nil {
					continue
				}
				ssrc := webrtc.SSRC(p.SSRC)
				m.Lock()
				m.sources[ssrc] = sender

				// log this with congestion control.
				ecn, err := rfc8698.CheckExplicitCongestionNotification(oob[:oobn])
				if err != nil {
					log.Error().Err(err).Msg("failed to check ecn")
					m.Unlock()
					continue
				}

				// get the twcc header sequence number.
				tccExt := &rtp.TransportCCExtension{}
				if ext := p.Header.GetExtension(5); ext != nil {
					if err := tccExt.Unmarshal(ext); err != nil {
						log.Error().Err(err).Msg("failed to unmarshal twcc extension")
						m.Unlock()
						continue
					}
				}

				fb := m.ccfb[sender.String()]
				if fb == nil {
					fb = &ccfb{
						sender: sender,
						stream: rfc8888.NewPacketStream(),
					}
					m.ccfb[sender.String()] = fb
				}
				if err := fb.stream.AddPacket(time.Now(), ssrc, tccExt.TransportSequence, ecn); err != nil {
					log.Error().Err(err).Msg("failed to add packet to congestion control")
				}
				m.Unlock()
			}
		}
	}()
	return rtpReader, rtcpReader, m
}

func (m *SSRCManager) handleRTCP(sender *net.UDPAddr, buf []byte, ts time.Time) error {
	// it's an rtcp packet.
	cp, err := rtcp.Unmarshal(buf)
	if err != nil {
		// not a valid rtcp packet.
		return err
	}
	// if it's a sender clock report, immediately respond with a receiver clock report.
	// additionally, by contract sender clocks are sent in separate packets so we don't forward.
	for _, p := range cp {
		switch p := p.(type) {
		case *rtcp.RawPacket:
			if p.Header().Type == rtcp.TypeTransportSpecificFeedback &&
				p.Header().Count == 29 {
				senderClockReport := &clock.SenderClock{}
				if err := senderClockReport.Unmarshal([]byte(*p)[4:]); err != nil {
					return err
				}
				receiverClockReport := &clock.ReceiverClock{
					LastSenderNTPTime: senderClockReport.SenderNTPTime,
					Delay:             x_time.GoDurationToNTP(time.Since(ts)),
				}
				payload, err := receiverClockReport.Marshal()
				if err != nil {
					return err
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
					return err
				}
				copy(buf, hData)
				copy(buf[len(hData):], payload)
				if _, err := m.conn.WriteToUDP(buf, sender); err != nil {
					log.Error().Err(err).Msg("failed to send congestion control packet")
				}
				return nil
			}
		}
	}
	if _, err := m.rtcpWriter.WriteRTCP(cp); err != nil {
		return err
	}
	return nil
}

// Write writes to the connection sending to only senders that have sent to that ssrc.
func (m *SSRCManager) WriteRTCP(pkts []rtcp.Packet) (int, error) {
	buf, err := rtcp.Marshal(pkts)
	if err != nil {
		return 0, err
	}
	m.RLock()
	for _, p := range pkts {
		for _, ssrc := range p.DestinationSSRC() {
			// forward this packet to that ssrc's source.
			if addr, ok := m.sources[webrtc.SSRC(ssrc)]; ok {
				if _, err := m.conn.WriteToUDP(buf, addr); err != nil {
					log.Error().Err(err).Msg("failed to send rtcp packet")
				}
			}
		}
	}
	m.RUnlock()
	return len(pkts), nil
}
