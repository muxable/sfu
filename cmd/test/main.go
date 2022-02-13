package main

import (
	"context"
	"log"
	"net"

	"github.com/muxable/transcoder/pkg/transcoder"
	sdk "github.com/pion/ion-sdk-go"
	"github.com/pion/webrtc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	tcConn, err := grpc.Dial("127.0.0.1:50050", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	tc, err := transcoder.NewClient(context.Background(), tcConn)
	if err != nil {
		panic(err)
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		panic(err)
	}

	tl, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: "video/H265",
		ClockRate: 90000,
		SDPFmtpLine: "sprop-vps=QAEMAf//AWAAAAMAkAAAAwAAAwA/ugJA;sprop-sps=QgEBAWAAAAMAkAAAAwAAAwA/oAoIDxZbpKTC//AAEAAQEAAAAwAQAAADAeCA;sprop-pps=RAHAcYES",
	}, "video", "h265")

	log.Printf("listening")

	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				log.Println(err)
				return
			}
			tl.Write(buf[:n])
		}
	}()

	tr, err := tc.Transcode(tl)
	if err != nil {
		panic(err)
	}

	tl2, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: "video/H264",
		ClockRate: 90000,
	}, "video", "h264")

	log.Printf("%v", tr.Codec())

	dial, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4445})

	go func() {
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				panic(err)
			}
			tl2.WriteRTP(p)
			buf, err := p.Marshal()
			if err != nil {
				panic(err)
			}
			dial.Write(buf)
		}
	}()

	connector := sdk.NewConnector("34.145.147.32:50051")
	rtc := sdk.NewRTC(connector)
	if err := rtc.Join("mugit", "mugit", sdk.NewJoinConfig().SetNoSubscribe().SetNoAutoSubscribe()); err != nil {
		panic(err)
	}

	rtc.Publish(tl2)

	select{}

}