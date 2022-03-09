package server

import (
	"fmt"
	"net"

	"github.com/muxable/sfu/pkg/av"
	"github.com/pion/webrtc/v3"
)

type TrackHandler interface {
	AddTrack(webrtc.TrackLocal) (*webrtc.RTPSender, error)
	RemoveTrack(*webrtc.RTPSender) error
}

func CopyTracks(sid string, pc TrackHandler, c *av.RTPMuxContext) error {
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
		rtpSender, err := pc.AddTrack(track)
		if err != nil {
			return err
		}
		go func() {
			buf := make([]byte, 1500)
			for {
				_, _, err := rtpSender.Read(buf)
				if err != nil {
					break
				}
			}
		}()
		defer pc.RemoveTrack(rtpSender)
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

var _ TrackHandler = (*webrtc.PeerConnection)(nil)

type TCPServer interface {
	Serve(net.Listener) error
}

type UDPServer interface {
	Serve(*net.UDPConn) error
}
