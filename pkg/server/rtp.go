package server

import (
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/muxable/sfu/internal/analyzer"
	"github.com/muxable/sfu/internal/buffer"
	"github.com/muxable/sfu/internal/sessionizer"
	av "github.com/muxable/sfu/pkg/av"
	"github.com/muxable/sfu/pkg/cdn"
	"github.com/muxable/sfu/pkg/codec"
	"github.com/pion/rtcp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
)

func RunRTPServer(addr string, th TrackHandler, videoCodec, audioCodec webrtc.RTPCodecCapability) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}

	zap.L().Info("listening for RTP", zap.String("addr", addr))

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

		analyze, err := analyzer.NewAnalyzer(50)
		if err != nil {
			return err
		}

		go func() {
			go io.Copy(analyze, source)
			go rtpio.DiscardRTCP.ReadRTCPFrom(analyze)

			params := analyze.ReadResults()

			codec, ok := codecs.FindByPayloadType(params.PayloadType)
			if !ok {
				return
			}

			jb := buffer.NewReorderBuffer(codec.ClockRate, 750*time.Millisecond)
			go rtpio.CopyRTP(jb, analyze)
			// write nacks periodically back to the sender
			nackTicker := time.NewTicker(250 * time.Millisecond)
			defer nackTicker.Stop()
			done := make(chan bool, 1)
			defer func() { done <- true }()
			go func() {
				prevMissing := make([]bool, 1<<16)
				senderSSRC := rand.Uint32()
				for {
					select {
					case <-nackTicker.C:
						missing := jb.MissingSequenceNumbers()
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
						if err := srv.WriteRTCP([]rtcp.Packet{nack}); err != nil {
							log.Error().Err(err).Msg("failed to write NACK")
						}
					case <-done:
						return
					}
				}
			}()

			log.Info().Str("CNAME", params.CNAME).Uint32("SSRC", uint32(source.SSRC)).Uint8("PayloadType", uint8(params.PayloadType)).Msg("new inbound stream")

			if codec.MimeType == webrtc.MimeTypeOpus {
				// pass the audio straight through to a track.
				tl, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, uuid.NewString(), params.CNAME)
				if err != nil {
					zap.L().Error("failed to create track", zap.Error(err))
					return
				}
				ctl := cdn.NewCDNTrackLocalStaticRTP(tl)
				remove, err := th(ctl)
				if err != nil {
					zap.L().Error("failed to add track", zap.Error(err))
					return
				}
				defer remove()
				for {
					p, err := jb.ReadRTP()
					if err != nil {
						return
					}
					if err := ctl.WriteRTP(p); err != nil {
						return
					}
				}
			} else {
				// construct the elements
				demux, err := av.NewRTPDemuxer(webrtc.RTPCodecParameters{
					PayloadType:        params.PayloadType,
					RTPCodecCapability: codec.RTPCodecCapability,
				}, jb)
				if err != nil {
					zap.L().Error("failed to create demuxer", zap.Error(err))
					return
				}
				streams := demux.Streams()
				decoders := make([]*av.DecodeContext, len(streams))
				encoders := make([]*av.EncodeContext, len(streams))
				parameters := make([]*av.AVCodecParameters, len(streams))
				for i, stream := range demux.Streams() {
					decoder, err := av.NewDecoder(demux, stream)
					if err != nil {
						zap.L().Error("failed to create decoder", zap.Error(err))
						return
					}
					encoder, err := av.NewEncoder(videoCodec, decoder)
					if err != nil {
						zap.L().Error("failed to create encoder", zap.Error(err))
						return
					}
					decoders[i] = decoder
					encoders[i] = encoder
					parameters[i] = av.NewAVCodecParametersFromEncoder(encoder)
				}
				mux, err := av.NewRTPMuxer(parameters)
				if err != nil {
					zap.L().Error("failed to create muxer", zap.Error(err))
					return
				}

				outParams, err := mux.RTPCodecParameters()
				if err != nil {
					zap.L().Error("failed to get codec parameters", zap.Error(err))
					return
				}
				trackSink, err := NewTrackSink(outParams, params.CNAME, th)
				if err != nil {
					zap.L().Error("failed to create sink", zap.Error(err))
					return
				}

				// wire them together
				for i := 0; i < len(streams); i++ {
					if decoder := decoders[i]; decoder != nil {
						demux.Sinks = append(demux.Sinks, &av.IndexedSink{AVPacketWriteCloser: decoder, Index: 0})
						decoder.Sink = encoders[i]
						encoders[i].Sink = &av.IndexedSink{AVPacketWriteCloser: mux, Index: 0}
					} else {
						demux.Sinks = append(demux.Sinks, &av.IndexedSink{AVPacketWriteCloser: mux, Index: 0})
					}
				}
				mux.Sink = trackSink

				// TODO: validate the construction

				// start the pipeline
				if err := demux.Run(); err != nil {
					if err != io.EOF {
						zap.L().Error("failed to run pipeline", zap.Error(err))
					}
					if err := demux.Close(); err != nil {
						zap.L().Error("failed to close pipeline", zap.Error(err))
					}
					return
				}
			}
		}()
	}
}
