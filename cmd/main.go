package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"os"

	"github.com/muxable/sfu/pkg/server"
	"github.com/muxable/transcoder/pkg/transcoder"
	sdk "github.com/pion/ion-sdk-go"
	"github.com/pion/webrtc/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func resolveTranscoder(addr string) *transcoder.Client {
	if addr == "" {
		return nil
	}
	tcConn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	tc, err := transcoder.NewClient(context.Background(), tcConn)
	if err != nil {
		panic(err)
	}

	return tc
}

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

	rtpAddr := flag.String("rtp", "0.0.0.0:5000", "The address to receive from")
	// webrtcAddr := flag.String("webrtc", "0.0.0.0:50051", "The address to receive from")
	rtmpAddr := flag.String("rtmp", "0.0.0.0:1935", "The address to receive from")
	toAddr := flag.String("to", "34.145.147.32:50051", "The address to send to")
	flag.Parse()

	tlCh := make(chan *webrtc.TrackLocalStaticRTP)

	connector := sdk.NewConnector(*toAddr)

	go runRTPServer(*rtpAddr, tlCh)
	go server.RunRTMPServer(*rtmpAddr, tlCh)
	// go server.RunSRTServer("0.0.0.0", 1935, tlCh)
	// go runWebRTCServer(*webrtcAddr, tlCh)

	for {
		tl, ok := <-tlCh
		if !ok {
			break
		}

		zap.L().Info("received track", zap.String("id", tl.ID()), zap.Any("codec", tl.Codec()))

		rtc := sdk.NewRTC(connector)
		if err := rtc.Join(tl.StreamID(), tl.ID(), sdk.NewJoinConfig().SetNoSubscribe().SetNoAutoSubscribe()); err != nil {
			zap.L().Error("failed to join", zap.Error(err))
			continue
		}
		if _, err := rtc.Publish(tl); err != nil {
			zap.L().Error("failed to publish", zap.Error(err))
			continue
		}
		zap.L().Info("published", zap.String("id", tl.ID()), zap.String("room", tl.StreamID()))
	}
}

func runRTPServer(addr string, out chan *webrtc.TrackLocalStaticRTP) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to resolve UDP address")
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen on UDP")
	}

	rtpServer := server.NewRTPServer()
	go func() {
		for {
			tl, err := rtpServer.AcceptTrackLocal()
			if err != nil {
				return
			}
			out <- tl
		}
	}()

	zap.L().Info("listening for RTP", zap.String("addr", addr))

	return rtpServer.Serve(conn)
}

// func runWebRTCServer(addr string, out chan webrtc.TrackLocal) error {
// 	grpcAddr, err := net.ResolveTCPAddr("tcp", addr)
// 	if err != nil {
// 		log.Fatal().Err(err).Msg("failed to resolve TCP address")
// 	}
// 	grpcConn, err := net.ListenTCP("tcp", grpcAddr)
// 	if err != nil {
// 		log.Fatal().Err(err).Msg("failed to listen on TCP")
// 	}

// 	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port:0})
// 	if err != nil {
// 		log.Fatal().Err(err).Msg("failed to listen on UDP")
// 	}

// 	zap.L().Info("listening for WebRTC", zap.String("addr", grpcAddr.String()))

// 	return server.ServeWebRTC(udpConn, grpcConn, out)
// }
