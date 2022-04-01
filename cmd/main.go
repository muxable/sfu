package main

import (
	"flag"
	"net"
	"net/http"
	"os"

	"github.com/muxable/sfu/pkg/cdn"
	"github.com/muxable/sfu/pkg/server"
	"github.com/pion/webrtc/v3"
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
	rtmpAddr := flag.String("rtmp", "0.0.0.0:1935", "The address to receive from")
	srtAddr := flag.String("srt", "0.0.0.0:1935", "The address to receive from")
	jsonAddr := flag.String("json", "0.0.0.0:7000", "The address to receive from")
	flag.Parse()

	node := cdn.NewLocalCDN()

	th := server.NewCDNHandler(node)

	videoCodec := webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeH264,
		ClockRate: 90000,
	}
	audioCodec := webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	}

	go server.RunRTPServer(*rtpAddr, th, videoCodec, audioCodec)
	go server.RunRTMPServer(*rtmpAddr, th, videoCodec, audioCodec)
	go server.RunSRTServer(*srtAddr, th, node, videoCodec, audioCodec)
	go server.RunJSONServer(*jsonAddr, node)

	select {}
}
