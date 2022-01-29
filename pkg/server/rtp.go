package server

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/muxable/ingress/internal/demuxer"
	"github.com/muxable/ingress/internal/sessionizer"
	"github.com/muxable/ingress/pkg/codec"
	"github.com/muxable/rtptools/pkg/rfc7005"
	"github.com/pion/rtcp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog/log"
)

type RTPServer struct {
	trackCh chan *NamedTrackLocal
}

func NewRTPServer() *RTPServer {
	return &RTPServer{
		trackCh: make(chan *NamedTrackLocal),
	}
}

func (s *RTPServer) Serve(conn *net.UDPConn) error {
	srv, err := sessionizer.NewSessionizer(conn, 1500)
	if err != nil {
		return err
	}

	codecs := codec.DefaultCodecSet()

	for {
		// source represents a unique ssrc
		source, err := srv.Accept()
		if err != nil {
			return err
		}

		senderSSRC := rand.Uint32()

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
				codecTicker := codec.Ticker()
				defer codecTicker.Stop()
				jb, jbRTP := rfc7005.NewJitterBuffer(codec.ClockRate, 1*time.Second, rtpIn)
				// write nacks periodically back to the sender
				nackTicker := time.NewTicker(100 * time.Millisecond)
				defer nackTicker.Stop()
				done := make(chan bool, 1)
				defer func() { done <- true }()
				go func() {
					prevMissing := make([]bool, 1<<16)
					for {
						select {
						case <-nackTicker.C:
							missing := jb.GetMissingSequenceNumbers(uint64(codec.ClockRate / 10))
							if len(missing) == 0 {
								break
							}
							retained := make([]uint16, 0)
							nextMissing := make([]bool, 1<<16)
							for _, seq := range missing {
								if prevMissing[seq] {
									retained = append(retained, seq)
								}
								nextMissing[seq] = true
							}
							prevMissing = nextMissing
							nack := &rtcp.TransportLayerNack{
								SenderSSRC: senderSSRC,
								MediaSSRC:  uint32(source.SSRC),
								Nacks:      rtcp.NackPairsFromSequenceNumbers(retained),
							}
							if err := source.WriteRTCP([]rtcp.Packet{nack}); err != nil {
								log.Error().Err(err).Msg("failed to write NACK")
							}
						case <-done:
							return
						}
					}
				}()

				log.Info().Str("CNAME", "").Uint32("SSRC", uint32(source.SSRC)).Uint8("PayloadType", uint8(pt)).Msg("new inbound stream")

				tid := fmt.Sprintf("%s-%d-%d", cname, source.SSRC, pt)
				track, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, tid, tid)
				if err != nil {
					log.Error().Err(err).Msg("failed to create track")
					return
				}

				go func() {
					prevSeq := uint16(0)
					for {
						p, err := jbRTP.ReadRTP()
						if err != nil {
							done <- true
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
				}()

				s.trackCh <- &NamedTrackLocal{
					TrackLocalStaticRTP: track,
					CNAME:               cname,
					TrackID:             tid,
				}
			})
		})
	}
}

// AcceptTrackLocal returns a track that can be sent to a peer connection.
func (s *RTPServer) AcceptTrackLocal() (*NamedTrackLocal, error) {
	ntl, ok := <-s.trackCh
	if !ok {
		return nil, io.EOF
	}
	return ntl, nil
}

var _ UDPServer = (*RTPServer)(nil)
var _ TrackProducer = (*RTPServer)(nil)
