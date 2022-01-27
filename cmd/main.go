package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/muxable/ingress/internal/demuxer"
	"github.com/muxable/ingress/internal/sessionizer"
	"github.com/muxable/ingress/pkg/codec"
	"github.com/muxable/rtpio/pkg/rtpio"
	"github.com/muxable/rtptools/pkg/rfc7005"
	"github.com/muxable/rtptools/pkg/x_ssrc"
	"github.com/muxable/transcoder/pkg/transcoder"
	sdk "github.com/pion/ion-sdk-go"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// The overall pipeline follows the following architecture:
// - receiver
// - cname demuxer
// - normalizer
// - jitter buffer + nack emitter
// - pt demuxer
// - depacketizer
// - transcoder
// - pt muxer (implicit)
// - sender
func main() {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	defer logger.Sync()
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	go func() {
		m := http.NewServeMux()
		m.Handle("/metrics", promhttp.Handler())
		srv := &http.Server{
			Handler: m,
		}

		metricsLis, err := net.Listen("tcp", ":8012")
		if err != nil {
			return
		}

		err = srv.Serve(metricsLis)
		if err != nil {
			return
		}
	}()
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	from := flag.String("from", "0.0.0.0:5000", "The address to receive from")
	to := flag.String("to", "34.145.147.32:50051", "The address to send to")
	flag.Parse()

	// receive inbound packets.
	udpAddr, err := net.ResolveUDPAddr("udp", *from)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to resolve UDP address")
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen on UDP")
	}

	log.Printf("listening on %s", udpAddr)

	srv := sessionizer.NewSessionizer(conn, 1500)

	for {
		conn, err := srv.Accept()
		if err != nil {
			panic(err)
		}

		senderSSRC := rand.Uint32()

		connector := sdk.NewConnector(*to)
		rtc := sdk.NewRTC(connector, sdk.DefaultConfig)

		rid := "mugit"

		if err := rtc.Join(rid, sdk.RandomKey(4), sdk.NewJoinConfig().SetNoSubscribe()); err != nil {
			panic(err)
		}
	}

	tcConn, err := grpc.Dial("transcode.mtun.io:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	tc, err := transcoder.NewClient(context.Background(), tcConn)
	if err != nil {
		panic(err)
	}

	codecs := codec.DefaultCodecSet()

	x_ssrc.NewDemultiplexer(time.Now, rtpReader, rtcpReader, func(ssrc webrtc.SSRC, rtpIn rtpio.RTPReader, rtcpIn rtpio.RTCPReader) {
		go rtpio.DiscardRTCP.ReadRTCPFrom(rtcpIn)
		demuxer.NewPayloadTypeDemuxer(time.Now, rtpIn, func(pt webrtc.PayloadType, rtpIn rtpio.RTPReader) {
			// match with a codec.
			codec, ok := codecs.FindByPayloadType(pt)
			if !ok {
				log.Warn().Uint32("SSRC", uint32(ssrc)).Uint8("PayloadType", uint8(pt)).Msg("demuxer unknown payload type")
				// we do need to consume all the packets though.
				rtpio.DiscardRTP.ReadRTPFrom(rtpIn)
			} else {
				log.Debug().Uint32("SSRC", uint32(ssrc)).Uint8("PayloadType", uint8(pt)).Msg("demuxer found new stream type")
			}
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
							MediaSSRC:  uint32(ssrc),
							Nacks:      rtcp.NackPairsFromSequenceNumbers(retained),
						}
						if err := rtcpWriter.WriteRTCP([]rtcp.Packet{nack}); err != nil {
							log.Error().Err(err).Msg("failed to write NACK")
						}
					case <-done:
						return
					}
				}
			}()

			log.Info().Str("CNAME", "").Uint32("SSRC", uint32(ssrc)).Uint8("PayloadType", uint8(pt)).Msg("new inbound stream")

			tid := fmt.Sprintf("%s-%d-%d", "mugit", ssrc, pt)
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

			transcodedRemote, err := tc.Transcode(track)
			if err != nil {
				log.Error().Err(err).Msg("failed to transcode")
				return
			}

			transcodedLocal, err := pipe(transcodedRemote)
			if err != nil {
				log.Error().Err(err).Msg("failed to pipe")
				return
			}

			if _, err := rtc.Publish(transcodedLocal); err != nil {
				log.Error().Err(err).Msg("failed to publish")
				return
			}

			<-done
		})
	})
}

func pipe(tr *webrtc.TrackRemote) (webrtc.TrackLocal, error) {
	tl, err := webrtc.NewTrackLocalStaticRTP(tr.Codec().RTPCodecCapability, tr.ID(), tr.StreamID())
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if err := tl.WriteRTP(p); err != nil {
				return
			}
		}
	}()
	return tl, nil

}
