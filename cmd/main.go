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
	// rtmpAddr := flag.String("rtmp", "0.0.0.0:1935", "The address to receive from")
	toAddr := flag.String("to", "34.145.147.32:50051", "The address to send to")
	tcAddr := flag.String("transcode", "", "The address of the transcoder")
	flag.Parse()

	tc := resolveTranscoder(*tcAddr)

	tlCh := make(chan *server.NamedTrackLocal)

	connector := sdk.NewConnector(*toAddr)

	go runRTPServer(*rtpAddr, tlCh)
	// go runRTMPServer(*rtmpAddr, tlCh)

	for {
		tl, ok := <-tlCh
		if !ok {
			break
		}

		zap.L().Info("received track", zap.String("id", tl.ID()), zap.Any("codec", tl.Codec()))

		rtc := sdk.NewRTC(connector)
		if err := rtc.Join(tl.CNAME, tl.TrackID, sdk.NewJoinConfig().SetNoSubscribe().SetNoAutoSubscribe()); err != nil {
			zap.L().Error("failed to join", zap.Error(err))
			continue
		}
		if tc == nil || tl.Kind() != webrtc.RTPCodecTypeVideo {
			if _, err := rtc.Publish(tl); err != nil {
				zap.L().Error("failed to publish", zap.Error(err))
				continue
			}
			zap.L().Info("published", zap.String("id", tl.ID()), zap.String("room", tl.CNAME))
		} else {
			transcodedRemote, err := tc.Transcode(tl)
			if err != nil {
				zap.L().Error("failed to transcode", zap.Error(err))
				continue
			}

			dial, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 23084})

			transcodedLocal, err := pipe(transcodedRemote, dial)

			zap.L().Info("transcoded", zap.String("id", transcodedLocal.ID()), zap.String("room", tl.CNAME), zap.Any("codec", transcodedLocal.Codec()))
			if err != nil {
				zap.L().Error("failed to pipe", zap.Error(err))
				continue
			}

			if _, err := rtc.Publish(transcodedLocal); err != nil {
				zap.L().Error("failed to publish", zap.Error(err))
				continue
			}
			zap.L().Info("published", zap.String("id", transcodedLocal.ID()), zap.String("room", tl.CNAME), zap.Any("codec", transcodedLocal.Codec()))
		}
	}
}

func pipe(tr *webrtc.TrackRemote, c *net.UDPConn) (*webrtc.TrackLocalStaticRTP, error) {
	tl, err := webrtc.NewTrackLocalStaticRTP(tr.Codec().RTPCodecCapability, tr.ID(), tr.StreamID())
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				log.Printf("failed to read rtp: %v", err)
				return
			}
			if err := tl.WriteRTP(p); err != nil {
				log.Printf("failed to write rtp: %v", err)
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
