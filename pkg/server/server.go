package server

import (
	"fmt"
	"net"

	"github.com/muxable/sfu/pkg/av"
	sdk "github.com/pion/ion-sdk-go"
	"github.com/pion/webrtc/v3"
)

type TrackHandler func(webrtc.TrackLocal) (func() error, error)

func NewPeerConnectionTrackHandler(pc *webrtc.PeerConnection) TrackHandler {
	return func(t webrtc.TrackLocal) (func() error, error) {
		s, err := pc.AddTrack(t)
		if err != nil {
			return nil, err
		}
		go func() {
			buf := make([]byte, 1500)
			for {
				_, _, err := s.Read(buf)
				if err != nil {
					break
				}
			}
		}()
		return func() error {
			return pc.RemoveTrack(s)
		}, nil
	}
}

func NewRTCTrackHandler(connector *sdk.Connector) TrackHandler {
	return func(t webrtc.TrackLocal) (func() error, error) {
		rtc, err := sdk.NewRTC(connector)
		if err != nil {
			return nil, err
		}
		if err := rtc.Join(t.StreamID(), t.ID(), sdk.NewJoinConfig().SetNoSubscribe().SetNoAutoSubscribe()); err != nil {
			return nil, err
		}
		s, err := rtc.Publish(t)
		if err != nil {
			return nil, err
		}
		return func() error {
			return rtc.UnPublish(s...)
		}, nil
	}
}

func CopyTracks(sid string, th TrackHandler, c *av.RTPMuxContext) error {
	if err := c.Initialize(); err != nil {
		return err
	}

	params, err := c.RTPCodecParameters()
	if err != nil {
		return err
	}

	// create local tracks.
	tracks := make(map[uint8]*webrtc.TrackLocalStaticRTP)
	for _, p := range params {
		if p == nil {
			continue
		}
		track, err := webrtc.NewTrackLocalStaticRTP(p.RTPCodecCapability, fmt.Sprintf("%s-%d", sid, p.PayloadType), sid)
		if err != nil {
			return err
		}
		tracks[uint8(p.PayloadType)] = track
		remove, err := th(track)
		if err != nil {
			return err
		}
		defer remove()
	}

	for {
		p, err := c.ReadRTP()
		if err != nil {
			return err
		}

		track, ok := tracks[p.PayloadType]
		if !ok {
			continue
		}
		if err := track.WriteRTP(p); err != nil {
			return err
		}
	}
}

type TCPServer interface {
	Serve(net.Listener) error
}

type UDPServer interface {
	Serve(*net.UDPConn) error
}
