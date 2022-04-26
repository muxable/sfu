package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"

	"github.com/muxable/sfu/api"
	"github.com/muxable/signal/pkg/signal"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func NewPeerConnection() *webrtc.PeerConnection {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeOpus, 48000, 2, "minptime=10;useinbandfec=1", nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	// videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"ccnack", ""}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{webrtc.MimeTypeVP8, 90000, 0, "", nil},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}

	i := &interceptor.Registry{}
	// if err := webrtc.ConfigureRTCPReports(i); err != nil {
	// 	panic(err)
	// }

	// if err := webrtc.ConfigureTWCCHeaderExtensionSender(m, i); err != nil {
	// 	panic(err)
	// }


	// // configure ccnack
	// generator, err := ccnack.NewGeneratorInterceptor()
	// if err != nil {
	// 	panic(err)
	// }

	// responder, err := ccnack.NewResponderInterceptor()
	// if err != nil {
	// 	panic(err)
	// }

	// m.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccnack"}, webrtc.RTPCodecTypeVideo)
	// m.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccnack", Parameter: "pli"}, webrtc.RTPCodecTypeVideo)
	// i.Add(responder)
	// i.Add(generator)

	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i)).NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		panic(err)
	}
	return pc
}

func main() {
	pc1 := NewPeerConnection()
	pc2 := NewPeerConnection()

	signaller1 := signal.NewSignaller(pc1)
	signaller2 := signal.NewSignaller(pc2)

	conn1, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn1.Close()

	conn2, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn2.Close()

	client1, err := api.NewSFUClient(conn1).Publish(context.Background())
	if err != nil {
		panic(err)
	}
	client2, err := api.NewSFUClient(conn2).Publish(context.Background())
	if err != nil {
		panic(err)
	}

	// Open a UDP Listener for RTP Packets on port 5004
	lis, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5004})
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	// Create a video track for half of the packets.
	vt1, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
	if err != nil {
		panic(err)
	}
	rtpSender1, err := pc1.AddTrack(vt1)
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			p, _, err := rtpSender1.ReadRTCP()
			if err != nil {
				return
			}
			log.Printf("RTCP: %v", p)
		}
	}()

	// Create a video track for another half
	vt2, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
	if err != nil {
		panic(err)
	}
	rtpSender2, err := pc2.AddTrack(vt2)
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			p, _, err := rtpSender2.ReadRTCP()
			if err != nil {
				return
			}
			log.Printf("RTCP: %v", p)
		}
	}()

	go func() {
		for {
			pb, err := signaller1.ReadSignal()
			if err != nil {
				panic(err)
			}
			if err := client1.Send(pb); err != nil {
				panic(err)
			}
		}
	}()

	go func() {
		for {
			pb, err := client1.Recv()
			if err != nil {
				panic(err)
			}
			if err := signaller1.WriteSignal(pb); err != nil {
				panic(err)
			}
		}
	}()

	go func() {
		for {
			pb, err := signaller2.ReadSignal()
			if err != nil {
				panic(err)
			}
			if err := client2.Send(pb); err != nil {
				panic(err)
			}
		}
	}()

	go func() {
		for {
			pb, err := client2.Recv()
			if err != nil {
				panic(err)
			}
			if err := signaller2.WriteSignal(pb); err != nil {
				panic(err)
			}
		}
	}()

	go signaller1.Renegotiate()
	go signaller2.Renegotiate()

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
		if rand.Intn(10) == 0 {
			// 10% packet loss
			continue
		}
		// randomly choose between vt1 and vt2
		if rand.Intn(2) == 0 {
			if _, err = vt1.Write(buf[:n]); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					// The peerConnection has been closed.
					return
				}

				panic(err)
			}
		} else {
			if _, err = vt2.Write(buf[:n]); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					// The peerConnection has been closed.
					return
				}

				panic(err)
			}
		}
	}
}
