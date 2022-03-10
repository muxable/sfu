package main

import (
	"flag"
	"net"
	"net/http"
	"os"

	"github.com/muxable/sfu/pkg/server"
	sdk "github.com/pion/ion-sdk-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
)

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

	connector := sdk.NewConnector(*toAddr)

	th := server.NewRTCTrackHandler(connector)

	go server.RunRTPServer(*rtpAddr, th)
	go server.RunRTMPServer(*rtmpAddr, th)

	select{}
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
