package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/muxable/sfu/api"
	"github.com/muxable/signal/pkg/signal"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		panic(err)
	}

	signaller := signal.NewSignaller(peerConnection)

	conn, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	client, err := api.NewSFUClient(conn).Publish(context.Background())
	if err != nil {
		panic(err)
	}

	// Open a UDP Listener for RTP Packets on port 5004
	lis, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5004})
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	// Create a video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
	if err != nil {
		panic(err)
	}
	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		panic(err)
	}

	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	go signaller.Renegotiate()

	go func() {
		for {
			pb, err := signaller.ReadSignal()
			if err != nil {
				zap.L().Error("failed to read signal", zap.Error(err))
				return
			}
			if err := client.Send(pb); err != nil {
				zap.L().Error("failed to send signal", zap.Error(err))
				return
			}
		}
	}()

	go func() {
		for {
			pb, err := client.Recv()
			if err != nil {
				panic(err)
			}
			if err := signaller.WriteSignal(pb); err != nil {
				panic(err)
			}
		}
	}()

	// Read RTP packets forever and send them to the WebRTC Client
	buf := make([]byte, 1600) // UDP MTU
	for {
		n, _, err := lis.ReadFromUDP(buf)
		if err != nil {
			panic(fmt.Sprintf("error during read: %s", err))
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(buf[:n]); err != nil {
			panic(fmt.Sprintf("error during unmarshal: %s", err))
		}
		if p.PayloadType != 112 {
			continue
		}
		if _, err = videoTrack.Write(buf[:n]); err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				// The peerConnection has been closed.
				return
			}

			panic(err)
		}
	}
}
