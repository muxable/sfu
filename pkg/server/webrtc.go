package server

// type WebRTCServer struct {
// 	trackCh chan *webrtc.TrackLocalStaticRTP
// 	api     *webrtc.API
// }

// func ServeWebRTC(conn *net.UDPConn, signal net.Listener, trackCh chan *webrtc.TrackLocalStaticRTP) error {
// 	mux, err := ssrc.NewSSRCMux(conn)
// 	if err != nil {
// 		return err
// 	}
// 	settingEngine := webrtc.SettingEngine{}
// 	settingEngine.SetICEUDPMux(webrtc.NewICEUDPMux(nil, mux))

// 	grpcServer := grpc.NewServer()
// 	api.RegisterSFUServer(grpcServer, &WebRTCServer{
// 		trackCh: trackCh,
// 		api:     webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine)),
// 	})
// 	grpc_health_v1.RegisterHealthServer(grpcServer, health.NewServer())

// 	return grpcServer.Serve(signal)
// }

// func (s *WebRTCServer) Signal(conn api.SFU_SignalServer) error {
// 	peerConnection, err := s.api.NewPeerConnection(webrtc.Configuration{
// 		ICEServers: []webrtc.ICEServer{
// 			{URLs: []string{"stun:stun.l.google.com:19302"}},
// 		},
// 	})
// 	if err != nil {
// 		return err
// 	}

// 	peerConnection.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
// 		go func() {
// 			buf := make([]byte, 1500)
// 			for {
// 				if _, _, err := r.Read(buf); err != nil {
// 					return
// 				}
// 			}
// 		}()

// 		tl, err := webrtc.NewTrackLocalStaticRTP(tr.Codec().RTPCodecCapability, tr.ID(), tr.StreamID())
// 		if err != nil {
// 			return
// 		}
// 		s.trackCh <- tl
// 	})

// 	signaller := signal.Negotiate(peerConnection)

// 	go func() {
// 		for {
// 			signal, err := signaller.ReadSignal()
// 			if err != nil {
// 				zap.L().Error("failed to read signal", zap.Error(err))
// 				return
// 			}
// 			if err := conn.Send(&api.Response{Signal: signal}); err != nil {
// 				zap.L().Error("failed to send signal", zap.Error(err))
// 				return
// 			}
// 		}
// 	}()

// 	for {
// 		in, err := conn.Recv()
// 		if err != nil {
// 			zap.L().Error("failed to receive", zap.Error(err))
// 			return nil
// 		}

// 		switch operation := in.Operation.(type) {
// 		case *api.Request_Key:
// 			return errors.New("NOT IMPLEMENTED")
// 		case *api.Request_Signal:
// 			if err := signaller.WriteSignal(operation.Signal); err != nil {
// 				return err
// 			}
// 		}
// 	}
// }

// var _ UDPServer = (*RTPServer)(nil)
// var _ TrackProducer = (*RTPServer)(nil)
