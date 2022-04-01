package server

import (
	"context"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/muxable/sfu/pkg/cdn"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

type Signal struct {
	*webrtc.SessionDescription `json:"sdp"`
	*webrtc.ICECandidateInit   `json:"candidate"`
	Error                      error `json:"error"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
} // use default options

func RunJSONServer(addr string, node *cdn.LocalCDN) error {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(
		webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeOpus, 48000, 2, "minptime=10;useinbandfec=1", nil},
			PayloadType:        111,
		}, webrtc.RTPCodecTypeAudio); err != nil {
		return err
	}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}

	// if err := m.RegisterCodec(
	// 	webrtc.RTPCodecParameters{
	// 		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeVP8, 90000, 0, "", nil},
	// 		PayloadType:        96,
	// 	}, webrtc.RTPCodecTypeVideo); err != nil {
	// 	return err
	// }

	// if err := m.RegisterCodec(
	// 	webrtc.RTPCodecParameters{
	// 		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeVP9, 90000, 0, "", nil},
	// 		PayloadType:        100,
	// 	}, webrtc.RTPCodecTypeVideo); err != nil {
	// 	return err
	// }

	if err := m.RegisterCodec(
		webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeH264, 90000, 0, "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", videoRTCPFeedback},
			PayloadType:        102,
		}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return err
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			zap.L().Error("error upgrading connection", zap.Error(err))
			return
		}
		defer c.Close()
		streamID := r.URL.Query().Get("sid")

		var mu sync.Mutex
		pc, err := api.NewPeerConnection(webrtc.Configuration{
			ICEServers: []webrtc.ICEServer{
				{
					URLs: []string{"stun:stun.l.google.com:19302"},
				},
			},
		})
		if err != nil {
			zap.L().Error("error creating peer connection", zap.Error(err))
			return
		}

		go func() {
			for {
				signal := &Signal{}
				if err := c.ReadJSON(signal); err != nil {
					break
				}

				if signal.SessionDescription != nil {
					if err := pc.SetRemoteDescription(*signal.SessionDescription); err != nil {
						zap.L().Error("error setting remote description", zap.Error(err))
						break
					}
				}
				if signal.ICECandidateInit != nil {
					if err := pc.AddICECandidate(*signal.ICECandidateInit); err != nil {
						zap.L().Error("error adding ice candidate", zap.Error(err))
						break
					}
				}
				if err != nil {
					zap.L().Error("error handling signal", zap.Error(err))
					break
				}
			}
		}()

		pc.OnNegotiationNeeded(func() {
			offer, err := pc.CreateOffer(nil)
			if err != nil {
				zap.L().Error("error creating offer", zap.Error(err))
				return
			}
			if err := pc.SetLocalDescription(offer); err != nil {
				zap.L().Error("error setting local description", zap.Error(err))
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if err := c.WriteJSON(Signal{SessionDescription: &offer}); err != nil {
				zap.L().Error("error writing offer", zap.Error(err))
				return
			}
		})

		pc.OnICECandidate(func(i *webrtc.ICECandidate) {
			if i == nil {
				return
			}
			candidate := i.ToJSON()
			mu.Lock()
			defer mu.Unlock()
			if err := c.WriteJSON(Signal{ICECandidateInit: &candidate}); err != nil {
				zap.L().Error("error writing ice candidate", zap.Error(err))
				return
			}
		})

		if _, err := pc.CreateDataChannel("data", nil); err != nil {
			zap.L().Error("error creating data channel", zap.Error(err))
			return
		}

		ctx, cancel := context.WithCancel(context.Background())

		c.SetCloseHandler(func(code int, text string) error {
			zap.L().Debug("connection closed", zap.Int("code", code), zap.String("text", text))
			cancel()
			return nil
		})

		sub := node.Subscribe(ctx, streamID)

		for {
			track := sub.NextTrack()
			zap.L().Debug("next track", zap.String("track", track.ID()))
			sender, err := pc.AddTrack(track)
			if err != nil {
				zap.L().Error("error adding track", zap.Error(err))
				mu.Lock()
				defer mu.Unlock()
				c.WriteJSON(Signal{Error: err})
				return
			}
			track.OnClose(func() {
				if err := pc.RemoveTrack(sender); err != nil {
					mu.Lock()
					defer mu.Unlock()
					c.WriteJSON(Signal{Error: err})
				}
			})
		}
	})
	return http.ListenAndServe(addr, nil)
}
