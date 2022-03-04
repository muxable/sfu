package main

import (
	"net"

	"github.com/muxable/sfu/internal/mpegts"
)

/*
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
	webrtcAddr := flag.String("webrtc", "0.0.0.0:50051", "The address to receive from")
	// rtmpAddr := flag.String("rtmp", "0.0.0.0:1935", "The address to receive from")
	toAddr := flag.String("to", "34.145.147.32:50051", "The address to send to")
	tcAddr := flag.String("transcode", "localhost:50050", "The address of the transcoder")
	flag.Parse()

	tc := resolveTranscoder(*tcAddr)

	tlCh := make(chan *server.NamedTrackLocal)

	connector := sdk.NewConnector(*toAddr)

	go runRTPServer(*rtpAddr, tlCh)
	// go runRTMPServer(*rtmpAddr, tlCh)
	go runWebRTCServer(*webrtcAddr, tlCh)

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
		if tc == nil {
			if _, err := rtc.Publish(tl); err != nil {
				zap.L().Error("failed to publish", zap.Error(err))
				continue
			}
			zap.L().Info("published", zap.String("id", tl.ID()), zap.String("room", tl.CNAME))
		} else {
			transcodedRemote, err := tc.Transcode(tl.TrackLocalStaticRTP)
			if err != nil {
				zap.L().Error("failed to transcode", zap.Error(err))
				continue
			}

			transcodedLocal, err := pipe(transcodedRemote)

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

func pipe(tr *webrtc.TrackRemote) (*webrtc.TrackLocalStaticRTP, error) {
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

func runWebRTCServer(addr string, out chan *server.NamedTrackLocal) error {
	grpcAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to resolve TCP address")
	}
	grpcConn, err := net.ListenTCP("tcp", grpcAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen on TCP")
	}

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port:0})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen on UDP")
	}

	zap.L().Info("listening for WebRTC", zap.String("addr", grpcAddr.String()))

	return server.ServeWebRTC(udpConn, grpcConn, out)
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
*/

func main() {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 5000})
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	d, err := mpegts.NewDemuxer(conn)
	if err != nil {
		panic(err)
	}

	dial, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002})
	if err != nil {
		panic(err)
	}

	for {
		p, err := d.ReadRTP()
		if err != nil {
			panic(err)
		}

		buf, err := p.Marshal()
		if err != nil {
			panic(err)
		}

		if _, err := dial.Write(buf); err != nil {
			panic(err)
		}
	}
}