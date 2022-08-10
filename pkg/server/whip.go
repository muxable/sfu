package server

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/muxable/sfu/api"
	"github.com/muxable/sfu/internal/buffer"
	av "github.com/muxable/sfu/pkg/av"
	"github.com/muxable/sfu/pkg/ccnack"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

func RunWHIPServer(addr string, trackHandler TrackHandler, videoCodec, audioCodec webrtc.RTPCodecCapability) error {

	http.HandleFunc("/whip/endpoint", func(w http.ResponseWriter, r *http.Request) {
		// CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Access-Control-Allow-Headers, Origin,Accept, X-Requested-With, Content-Type, Access-Control-Request-Method, Access-Control-Request-Headers")

		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST")
			return
		}

		// When calling the endpoint, only POST actually performs
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}

		server := &WHIPServer{
			trackHandler: trackHandler,
			videoCodec:   videoCodec,
			audioCodec:   audioCodec,
			counts:       make(map[string]uint16),
			buffers:      make(map[string]*buffer.ReorderBuffer),
			sources:      make(map[string]map[webrtc.SSRC]*webrtc.PeerConnection),
		}

		sdp, err := server.Publish(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		w.Write(sdp)
		return
	})

	return http.ListenAndServe(addr, nil)
}

type WHIPServer struct {
	api.UnimplementedSFUServer
	sync.Mutex

	trackHandler           TrackHandler
	videoCodec, audioCodec webrtc.RTPCodecCapability

	counts  map[string]uint16
	buffers map[string]*buffer.ReorderBuffer
	sources map[string]map[webrtc.SSRC]*webrtc.PeerConnection
}

func (s *WHIPServer) newTranscoder(codec webrtc.RTPCodecParameters, streamID string, src rtpio.RTPReader) (*av.DemuxContext, error) {
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

	return demux, nil
}

func (s *WHIPServer) Publish(body []byte) ([]byte, error) {
	m := &webrtc.MediaEngine{}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"ccnack", ""}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeH265, 90000, 0, "", videoRTCPFeedback},
		PayloadType:        119,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeOpus, 48000, 2, "minptime=10;useinbandfec=1", nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	i := &interceptor.Registry{}

	if err := webrtc.ConfigureRTCPReports(i); err != nil {
		return nil, err
	}

	if err := webrtc.ConfigureTWCCSender(m, i); err != nil {
		return nil, err
	}

	// configure ccnack
	generator, err := ccnack.NewGeneratorInterceptor()
	if err != nil {
		return nil, err
	}

	responder, err := ccnack.NewResponderInterceptor()
	if err != nil {
		return nil, err
	}

	m.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccnack"}, webrtc.RTPCodecTypeVideo)
	m.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccnack", Parameter: "pli"}, webrtc.RTPCodecTypeVideo)
	i.Add(responder)
	i.Add(generator)

	log.Printf("creating peer connection")

	// Because docker
	se := webrtc.SettingEngine{}
	se.SetNAT1To1IPs([]string{"127.0.0.1"}, webrtc.ICECandidateTypeHost)
	se.SetEphemeralUDPPortRange(5000, 5200)

	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i)).NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		return nil, err
	}

	pc.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
		key := fmt.Sprintf("%s:%s:%s", tr.StreamID(), tr.ID(), tr.RID())

		log.Printf("got track %s", key)

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
				for nack := range buffer.Nacks(100 * time.Millisecond) {
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

	nerr := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(body),
	})
	if err != nil {
		return nil, nerr
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)

	err = pc.SetLocalDescription(answer)
	if err != nil {
		return nil, err
	}

	<-gatherComplete

	return []byte(pc.CurrentLocalDescription().SDP), nil
}
