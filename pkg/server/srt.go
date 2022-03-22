package server

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/haivision/srtgo"
	av "github.com/muxable/sfu/pkg/av"
	"github.com/muxable/sfu/pkg/cdn"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

func parseStreamID(sid string) (map[string]string, error) {
	const header = "#!::"

	if !strings.HasPrefix(sid, header) {
		return map[string]string{
			"r": sid,
			"m": "request",
		}, nil
	}

	items := map[string]string{"m": "request"}
	for _, item := range strings.Split(sid[len(header):], ",") {
		kv := strings.Split(item, "=")
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid stream id %s", sid)
		}
		items[kv[0]] = kv[1]
	}
	return items, nil
}

func RunSRTServer(addr string, trackHandler TrackHandler, node *cdn.LocalCDN) error {
	zap.L().Info("listening for SRT", zap.String("addr", addr))

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	options := map[string]string{
		"transtype": "live",
	}

	sck := srtgo.NewSrtSocket(udpAddr.IP.String(), uint16(udpAddr.Port), options)
	defer sck.Close()
	if err := sck.Listen(1); err != nil {
		return err
	}
	for {
		conn, _, err := sck.Accept()
		if err != nil {
			return err
		}
		go func() {
			if err := handleConn(conn, trackHandler, node); err != nil {
				zap.L().Error("failed to handle connection", zap.Error(err))
			}
			conn.Close()
		}()
	}
}

func handleConn(conn *srtgo.SrtSocket, trackHandler TrackHandler, node *cdn.LocalCDN) error {
	sid, err := conn.GetSockOptString(srtgo.SRTO_STREAMID)
	if err != nil {
		return err
	}
	items, err := parseStreamID(sid)
	if err != nil {
		return err
	}

	if items["r"] == "" {
		return fmt.Errorf("missing stream id %s", sid)
	}
	if items["m"] == "publish" {
		videoCodec := webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: 90000,
		}
		audioCodec := webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		}

		// construct the elements
		demux, err := av.NewRawDemuxer(conn)
		if err != nil {
			return err
		}
		streams := demux.Streams()
		decoders := make([]*av.DecodeContext, len(streams))
		converters := make([]*av.ResampleContext, len(streams))
		encoders := make([]*av.EncodeContext, len(streams))
		parameters := make([]*av.AVCodecParameters, len(streams))
		for i, stream := range demux.Streams() {
			decoder, err := av.NewDecoder(demux, stream)
			if err != nil {
				return err
			}
			switch stream.AVMediaType() {
			case av.AVMediaTypeVideo:
				encoder, err := av.NewEncoder(videoCodec, decoder)
				if err != nil {
					return err
				}
				decoders[i] = decoder
				encoders[i] = encoder
				parameters[i] = av.NewAVCodecParametersFromEncoder(encoder)
			case av.AVMediaTypeAudio:
				encoder, err := av.NewEncoder(audioCodec, decoder)
				if err != nil {
					return err
				}
				resampler, err := av.NewResampler(decoder, encoder)
				if err != nil {
					return err
				}
				decoders[i] = decoder
				converters[i] = resampler
				encoders[i] = encoder
				parameters[i] = av.NewAVCodecParametersFromEncoder(encoder)
			default:
				zap.L().Error("unsupported media type", zap.Int("media_type", int(stream.AVMediaType())))
			}
		}
		mux, err := av.NewRTPMuxer(parameters)
		if err != nil {
			return err
		}
		params, err := mux.RTPCodecParameters()
		if err != nil {
			return err
		}
		trackSink, err := NewTrackSink(params, items["r"], trackHandler)
		if err != nil {
			return err
		}

		// wire them together
		for i := 0; i < len(streams); i++ {
			demux.Sinks = append(demux.Sinks, &av.IndexedSink{AVPacketWriteCloser: decoders[i], Index: 0})
			if resampler := converters[i]; resampler != nil {
				decoders[i].Sink = resampler
				resampler.Sink = encoders[i]
			} else {
				decoders[i].Sink = encoders[i]
			}
			encoders[i].Sink = &av.IndexedSink{AVPacketWriteCloser: mux, Index: i}
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
		}
	} else if items["m"] == "request" {
		listeners := make([]string, 0)
		demuxers := make([]*av.DemuxContext, 0)
		parameters := make([]*av.AVCodecParameters, 0)
		for i, track := range node.Get(items["r"]) {
			log.Printf("got track %v", track)
			pt := webrtc.PayloadType(96 + i)
			r, w := rtpio.RTPPipe()
			listeners = append(listeners, track.AddListener(pt, w))
			demux, err := av.NewRTPDemuxer(webrtc.RTPCodecParameters{
				PayloadType:        pt,
				RTPCodecCapability: track.Codec(),
			}, r)
			if err != nil {
				return err
			}
			demuxers = append(demuxers, demux)
			parameters = append(parameters, av.NewAVCodecParametersFromStream(demux.Streams()[0]))
		}
		mux, err := av.NewRawMuxer("webm", parameters)
		if err != nil {
			return err
		}

		// wire them together
		for i, demux := range demuxers {
			demux.Sinks = []*av.IndexedSink{{AVPacketWriteCloser: mux, Index: i}}
		}
		mux.Sink = &SrtSocketCloser{conn}

		var wg sync.WaitGroup
		for _, demux := range demuxers {
			wg.Add(1)
			go func(demux *av.DemuxContext) {
				defer wg.Done()
				if err := demux.Run(); err != nil {
					if err != io.EOF {
						zap.L().Error("failed to run pipeline", zap.Error(err))
					}
				}
			}(demux)
		}
		wg.Wait()
	}
	return nil
}

type SrtSocketCloser struct {
	*srtgo.SrtSocket
}

func (s *SrtSocketCloser) Close() error {
	s.SrtSocket.Close()
	return nil
}
