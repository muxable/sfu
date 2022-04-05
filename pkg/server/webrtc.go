package server

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/muxable/sfu/api"
	"github.com/muxable/sfu/internal/buffer"
	av "github.com/muxable/sfu/pkg/av"
	"github.com/muxable/signal/pkg/signal"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func RunWebRTCServer(addr string, trackHandler TrackHandler, videoCodec, audioCodec webrtc.RTPCodecCapability) error {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return err
	}

	zap.L().Info("listening for WebRTC", zap.String("addr", addr))

	srv := grpc.NewServer()
	api.RegisterSFUServer(srv, &SFUServer{
		trackHandler: trackHandler,
		videoCodec:   videoCodec,
		audioCodec:   audioCodec,
		counts:       make(map[string]uint16),
		buffers:      make(map[string]*buffer.ReorderBuffer),
	})
	return srv.Serve(conn)
}

type SFUServer struct {
	api.UnimplementedSFUServer
	sync.Mutex

	trackHandler           TrackHandler
	videoCodec, audioCodec webrtc.RTPCodecCapability

	counts  map[string]uint16
	buffers map[string]*buffer.ReorderBuffer
}

type trackWrapper struct {
	tr *webrtc.TrackRemote
}

func (t *trackWrapper) ReadRTP() (*rtp.Packet, error) {
	p, _, err := t.tr.ReadRTP()
	return p, err
}

func (s *SFUServer) newTranscoder(codec webrtc.RTPCodecParameters, streamID string, src rtpio.RTPReader) (*av.DemuxContext, error) {
	demux, err := av.NewRTPDemuxer(codec, src)
	if err != nil {
		return nil, err
	}
	streams := demux.Streams()
	decoders := make([]*av.DecodeContext, len(streams))
	converters := make([]*av.ResampleContext, len(streams))
	encoders := make([]*av.EncodeContext, len(streams))
	parameters := make([]*av.AVCodecParameters, len(streams))
	for i, stream := range demux.Streams() {
		decoder, err := av.NewDecoder(demux, stream)
		if err != nil {
			return nil, err
		}
		switch stream.AVMediaType() {
		case av.AVMediaTypeVideo:
			encoder, err := av.NewEncoder(s.videoCodec, decoder)
			if err != nil {
				return nil, err
			}
			decoders[i] = decoder
			encoders[i] = encoder
			parameters[i] = av.NewAVCodecParametersFromEncoder(encoder)
		case av.AVMediaTypeAudio:
			encoder, err := av.NewEncoder(s.audioCodec, decoder)
			if err != nil {
				return nil, err
			}
			resampler, err := av.NewResampler(decoder, encoder)
			if err != nil {
				return nil, err
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
		return nil, err
	}
	params, err := mux.RTPCodecParameters()
	if err != nil {
		return nil, err
	}
	trackSink, err := NewTrackSink(params, streamID, s.trackHandler)
	if err != nil {
		return nil, err
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

	return demux, nil
}

func (s *SFUServer) Publish(srv api.SFU_PublishServer) error {
	m := &webrtc.MediaEngine{}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeH265, 90000, 0, "", videoRTCPFeedback},
		PayloadType:        119,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}

	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return err
	}

	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i)).NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		return err
	}

	pc.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
		s.Lock()

		key := fmt.Sprintf("%s:%s:%s", tr.StreamID(), tr.ID(), tr.RID())

		if s.counts[key] == 0 {
			buffer := buffer.NewReorderBuffer(tr.Codec().ClockRate, 5*time.Second)

			go rtpio.CopyRTP(buffer, &trackWrapper{tr})

			transcoder, err := s.newTranscoder(tr.Codec(), tr.StreamID(), buffer)
			if err != nil {
				zap.L().Error("failed to create transcoder", zap.Error(err))
				s.Unlock()
				return
			}

			go transcoder.Run()

			s.buffers[key] = buffer
		} else {
			go rtpio.CopyRTP(s.buffers[key], &trackWrapper{tr})
		}
		s.counts[key]++

		s.Unlock()

		for {
			_, _, err := r.ReadRTCP()
			if err != nil {
				break
			}
		}

		s.Lock()

		s.counts[key]--
		if s.counts[key] == 0 {
			if err := s.buffers[key].Close(); err != nil {
				zap.L().Error("failed to close buffer", zap.Error(err))
			}
			s.buffers[key] = nil
		}

		s.Unlock()
	})

	signaller := signal.NewSignaller(pc)

	// since this is receive only, binding OnNegotiationNeeded is not necessary.

	go func() {
		for {
			pb, err := signaller.ReadSignal()
			if err != nil {
				zap.L().Error("failed to read signal", zap.Error(err))
				return
			}
			if err := srv.Send(pb); err != nil {
				zap.L().Error("failed to send signal", zap.Error(err))
				return
			}
		}
	}()

	for {
		pb, err := srv.Recv()
		if err != nil {
			zap.L().Error("failed to receive signal", zap.Error(err))
			return err
		}
		if err := signaller.WriteSignal(pb); err != nil {
			zap.L().Error("failed to write signal", zap.Error(err))
			return err
		}
	}
}
