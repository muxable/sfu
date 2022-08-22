package main

import (
	"flag"
	"net"
	"net/http"

	"github.com/muxable/sfu/pkg/cdn"
	"github.com/muxable/sfu/pkg/server"
	"github.com/pion/webrtc/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

	rtmpAddr := flag.String("rtmp", "0.0.0.0:1935", "The address to receive from")
	srtAddr := flag.String("srt", "0.0.0.0:1935", "The address to receive from")
	jsonAddr := flag.String("json", "0.0.0.0:7000", "The address to receive from")
	webrtcAddr := flag.String("webrtc", "0.0.0.0:50051", "The address to receive from")
	whipAddr := flag.String("whip", "0.0.0.0:8989", "The address to receive from")
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

	// Probably fine to use the base ones, but just testing
	webrtcVideoCodec := webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeVP8,
		ClockRate: 90000,
	}

	go server.RunRTMPServer(*rtmpAddr, th, videoCodec, audioCodec)
	go server.RunSRTServer(*srtAddr, th, node, videoCodec, audioCodec)
	go server.RunJSONServer(*jsonAddr, node)
	go server.RunWebRTCServer(*webrtcAddr, th, videoCodec, audioCodec)
	go server.RunWHIPServer(*whipAddr, th, webrtcVideoCodec, audioCodec)

	select {}
}
