package server

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/muxable/sfu/api"
	"github.com/muxable/sfu/internal/buffer"
	av "github.com/muxable/sfu/pkg/av"
	"github.com/muxable/sfu/pkg/ccnack"
	"github.com/muxable/signal/pkg/signal"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
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
		sources:      make(map[string]map[webrtc.SSRC]*webrtc.PeerConnection),
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
	sources map[string]map[webrtc.SSRC]*webrtc.PeerConnection
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
	decoders, err := demux.NewDecoders()
	if err != nil {
		return nil, err
	}
	configs, err := decoders.MapEncoderConfigurations(&av.EncoderConfiguration{Codec: s.audioCodec}, &av.EncoderConfiguration{Codec: s.videoCodec})
	if err != nil {
		return nil, err
	}
	encoders, err := decoders.NewEncoders(configs)
	if err != nil {
		return nil, err
	}
	mux, err := encoders.NewRTPMuxer()
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
	mux.Sink = trackSink

	// TODO: validate the construction

	return demux, nil
}

func (s *SFUServer) Publish(srv api.SFU_PublishServer) error {
	m := &webrtc.MediaEngine{}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"ccnack", ""}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeH265, 90000, 0, "", videoRTCPFeedback},
		PayloadType:        119,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeOpus, 48000, 2, "minptime=10;useinbandfec=1", nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}

	i := &interceptor.Registry{}

	if err := webrtc.ConfigureRTCPReports(i); err != nil {
		return err
	}

	if err := webrtc.ConfigureTWCCSender(m, i); err != nil {
		return err
	}

	// configure ccnack
	generator, err := ccnack.NewGeneratorInterceptor()
	if err != nil {
		return err
	}

	responder, err := ccnack.NewResponderInterceptor()
	if err != nil {
		return err
	}

	m.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccnack"}, webrtc.RTPCodecTypeVideo)
	m.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccnack", Parameter: "pli"}, webrtc.RTPCodecTypeVideo)
	i.Add(responder)
	i.Add(generator)

	log.Printf("creating peer connection")

	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i)).NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		return err
	}

	pc.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
		key := fmt.Sprintf("%s:%s:%s", tr.StreamID(), tr.ID(), tr.RID())

		log.Printf("got track %s", key)
		s.Lock()

		if s.counts[key] == 0 {
			buffer := buffer.NewReorderBuffer(tr.Codec().ClockRate, 1*time.Second)

			go rtpio.CopyRTP(buffer, &trackWrapper{tr})

			go func() {
				transcoder, err := s.newTranscoder(tr.Codec(), tr.StreamID(), buffer)
				if err != nil {
					zap.L().Error("failed to create transcoder", zap.Error(err))
					s.Unlock()
					return
				}

				if err := transcoder.Run(); err != nil {
					zap.L().Error("failed to run transcoder", zap.Error(err))
				}
			}()

			go func() {
				for nack := range buffer.Nacks(100*time.Millisecond) {
					s.Lock()
					sources := s.sources[key]
					if len(sources) == 0 {
						zap.L().Warn("no sources for nack", zap.String("key", key))
						s.Unlock()
						continue
					}

					// pick a random source
					var ssrcs []webrtc.SSRC
					for ssrc := range sources {
						ssrcs = append(ssrcs, ssrc)
					}
					ssrc := ssrcs[rand.Intn(len(ssrcs))]
					nack.MediaSSRC = uint32(ssrc)
					if err := sources[ssrc].WriteRTCP([]rtcp.Packet{nack}); err != nil {
						zap.L().Error("failed to write nack", zap.Error(err))
					}

					s.Unlock()
				}
			}()

			s.buffers[key] = buffer
			s.sources[key] = make(map[webrtc.SSRC]*webrtc.PeerConnection)
		} else {
			go rtpio.CopyRTP(s.buffers[key], &trackWrapper{tr})
		}
		s.counts[key]++
		s.sources[key][tr.SSRC()] = pc

		s.Unlock()

		for {
			_, _, err := r.ReadRTCP()
			if err != nil {
				break
			}
		}

		s.Lock()

		s.counts[key]--
		delete(s.sources[key], tr.SSRC())
		if s.counts[key] == 0 {
			if err := s.buffers[key].Close(); err != nil {
				zap.L().Error("failed to close buffer", zap.Error(err))
			}
			s.buffers[key] = nil
			s.sources[key] = nil
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
