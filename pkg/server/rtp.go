package server

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/muxable/sfu/internal/demuxer"
	"github.com/muxable/sfu/internal/sessionizer"
	"github.com/muxable/sfu/pkg/codec"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog/log"
)

type RTPServer struct {
	trackCh chan *webrtc.TrackLocalStaticRTP
}

func NewRTPServer() *RTPServer {
	return &RTPServer{
		trackCh: make(chan *webrtc.TrackLocalStaticRTP),
	}
}

func (s *RTPServer) Serve(conn *net.UDPConn) error {
	srv, err := sessionizer.NewSessionizer(conn, 1500)
	if err != nil {
		return err
	}

	codecs := codec.DefaultCodecSet()

	// r, w := rtpio.RTPPipe()
	// go srtsink.NewSRTSink(r)

	for {
		// source represents a unique ssrc
		source, err := srv.Accept()
		if err != nil {
			return err
		}

		// senderSSRC := rand.Uint32()

		// this api is a bit awkward, but is less insane than lots of for loops.
		go demuxer.NewCNAMEDemuxer(time.Now, source, source, func(cname string, rtpIn rtpio.RTPReader, rtcpIn rtpio.RTCPReader) {
			go rtpio.DiscardRTCP.ReadRTCPFrom(rtcpIn)
			go demuxer.NewPayloadTypeDemuxer(time.Now, rtpIn, func(pt webrtc.PayloadType, rtpIn rtpio.RTPReader) {
				// match with a codec.
				codec, ok := codecs.FindByPayloadType(pt)
				if !ok {
					log.Warn().Uint32("SSRC", uint32(source.SSRC)).Uint8("PayloadType", uint8(pt)).Msg("demuxer unknown payload type")
					// we do need to consume all the packets though.
					go rtpio.DiscardRTP.ReadRTPFrom(rtpIn)
					return
				}
				log.Debug().Uint32("SSRC", uint32(source.SSRC)).Uint8("PayloadType", uint8(pt)).Msg("demuxer found new stream type")
				// jb, jbRTP := rfc7005.NewJitterBuffer(codec.ClockRate, 750*time.Millisecond, rtpIn)
				// write nacks periodically back to the sender
				nackTicker := time.NewTicker(100 * time.Millisecond)
				defer nackTicker.Stop()
				done := make(chan bool, 1)
				defer func() { done <- true }()
				// go func() {
				// 	prevMissing := make([]bool, 1<<16)
				// 	for {
				// 		select {
				// 		case <-nackTicker.C:
				// 			missing := jb.GetMissingSequenceNumbers(uint64(codec.ClockRate / 10))
				// 			if len(missing) == 0 {
				// 				break
				// 			}
				// 			retained := make([]uint16, 0)
				// 			nextMissing := make([]bool, 1<<16)
				// 			for _, seq := range missing {
				// 				if prevMissing[seq] {
				// 					retained = append(retained, seq)
				// 				}
				// 				nextMissing[seq] = true
				// 			}
				// 			prevMissing = nextMissing
				// 			nack := &rtcp.TransportLayerNack{
				// 				SenderSSRC: senderSSRC,
				// 				MediaSSRC:  uint32(source.SSRC),
				// 				Nacks:      rtcp.NackPairsFromSequenceNumbers(retained),
				// 			}
				// 			if err := source.WriteRTCP([]rtcp.Packet{nack}); err != nil {
				// 				log.Error().Err(err).Msg("failed to write NACK")
				// 			}
				// 		case <-done:
				// 			return
				// 		}
				// 	}
				// }()

				log.Info().Str("CNAME", "").Uint32("SSRC", uint32(source.SSRC)).Uint8("PayloadType", uint8(pt)).Msg("new inbound stream")

				tid := fmt.Sprintf("%s-%d-%d", cname, source.SSRC, pt)
				track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
					MimeType: codec.MimeType,
					ClockRate: codec.ClockRate,
					Channels: codec.Channels,
				}, tid, cname)
				if err != nil {
					log.Error().Err(err).Msg("failed to create track")
					return
				}

				s.trackCh <- track
				
				prevSeq := uint16(0)
				for {
					p, err := rtpIn.ReadRTP()
					if err != nil {
						return
					}
					if p.SequenceNumber != prevSeq+1 {
						log.Warn().Uint16("PrevSeq", prevSeq).Uint16("CurrSeq", p.SequenceNumber).Msg("missing packet")
					}
					prevSeq = p.SequenceNumber
					if err := track.WriteRTP(p); err != nil {
						log.Warn().Err(err).Msg("failed to write sample")
					}
				}
			})
		})
	}
}

// AcceptTrackLocal returns a track that can be sent to a peer connection.
func (s *RTPServer) AcceptTrackLocal() (*webrtc.TrackLocalStaticRTP, error) {
	ntl, ok := <-s.trackCh
	if !ok {
		return nil, io.EOF
	}
	return ntl, nil
}

var _ UDPServer = (*RTPServer)(nil)
var _ TrackProducer = (*RTPServer)(nil)
