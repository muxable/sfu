package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"os"

	"github.com/muxable/ingress/pkg/server"
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

	rtpAddr := flag.String("rtp", "0.0.0.0:5000", "The address to receive from")
	// rtmpAddr := flag.String("rtmp", "0.0.0.0:1935", "The address to receive from")
	toAddr := flag.String("to", "34.145.147.32:50051", "The address to send to")
	tcAddr := flag.String("transcode", "transcode.mtun.io:50051", "The address of the transcoder")
	flag.Parse()

	tcConn, err := grpc.Dial(*tcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	tc, err := transcoder.NewClient(context.Background(), tcConn)
	if err != nil {
		panic(err)
	}

	tlCh := make(chan *server.NamedTrackLocal)

	connector := sdk.NewConnector(*toAddr)

	go runRTPServer(*rtpAddr, tlCh)
	// go runRTMPServer(*rtmpAddr, tlCh)

	for {
		tl, ok := <-tlCh
		if !ok {
			break
		}

		transcodedRemote, err := tc.Transcode(tl)
		if err != nil {
			zap.L().Error("failed to transcode", zap.Error(err))
			continue
		}

		transcodedLocal, err := pipe(transcodedRemote)
		if err != nil {
			zap.L().Error("failed to pipe", zap.Error(err))
			continue
		}

		rtc := sdk.NewRTC(connector, sdk.DefaultConfig)
		if err := rtc.Join(tl.Name, tl.Name, sdk.NewJoinConfig().SetNoSubscribe().SetNoAutoSubscribe()); err != nil {
			zap.L().Error("failed to join", zap.Error(err))
			continue
		}
		if _, err := rtc.Publish(transcodedLocal); err != nil {
			zap.L().Error("failed to publish", zap.Error(err))
			continue
		}
		zap.L().Info("published", zap.String("name", tl.Name))
	}
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

func runRTPServer(addr string, out chan *server.NamedTrackLocal) error {
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

// func runRTMPServer(addr string, out chan *server.NamedTrackLocal) error {
// 	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
// 	if err != nil {
// 		log.Fatal().Err(err).Msg("failed to resolve TCP address")
// 	}
// 	conn, err := net.ListenTCP("tcp", tcpAddr)
// 	if err != nil {
// 		log.Fatal().Err(err).Msg("failed to listen on TCP")
// 	}

// 	rtmpServer := server.NewRTMPServer()
// 	go func() {
// 		for {
// 			tl, err := rtmpServer.AcceptTrackLocal()
// 			if err != nil {
// 				return
// 			}
// 			out <- tl
// 		}
// 	}()

// 	zap.L().Info("listening for RTMP", zap.String("addr", addr))

// 	return rtmpServer.Serve(conn)
// }
