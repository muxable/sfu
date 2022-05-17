package server

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/muxable/sfu/pkg/cdn"
	sdk "github.com/pion/ion-sdk-go"
	"github.com/pion/rtp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
)

type TrackHandler func(*cdn.CDNTrackLocalStaticRTP) (func() error, error)

func NewPeerConnectionTrackHandler(pc *webrtc.PeerConnection) TrackHandler {
	return func(t *cdn.CDNTrackLocalStaticRTP) (func() error, error) {
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
	return func(t *cdn.CDNTrackLocalStaticRTP) (func() error, error) {
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
			if err := rtc.UnPublish(s...); err != nil {
				return err
			}
			rtc.Close()
			return nil
		}, nil
	}
}

func NewCDNHandler(node *cdn.LocalCDN) TrackHandler {
	return func(tl *cdn.CDNTrackLocalStaticRTP) (func() error, error) {
		unpublish := node.Publish(tl)
		return func() error {
			unpublish()
			return nil
		}, nil
	}
}

type TrackSink struct {
	tracks  map[uint8]*cdn.CDNTrackLocalStaticRTP
	closers []func() error
}

func NewTrackSink(params []*webrtc.RTPCodecParameters, sid string, th TrackHandler) (*TrackSink, error) {
	// create local tracks.
	tracks := make(map[uint8]*cdn.CDNTrackLocalStaticRTP)
	closers := make([]func() error, len(params))
	for i, p := range params {
		if p == nil {
			continue
		}
		track, err := webrtc.NewTrackLocalStaticRTP(p.RTPCodecCapability, uuid.NewString(), sid)
		if err != nil {
			return nil, err
		}
		cdntrack := cdn.NewCDNTrackLocalStaticRTP(track)
		tracks[uint8(p.PayloadType)] = cdntrack
		remove, err := th(cdntrack)
		if err != nil {
			return nil, err
		}
		closers[i] = remove
	}

	return &TrackSink{tracks: tracks, closers: closers}, nil
}

func (s *TrackSink) WriteRTP(p *rtp.Packet) error {
	track := s.tracks[p.PayloadType]
	if track == nil {
		return fmt.Errorf("no track for payload type %d: %v", p.PayloadType, s.tracks)
	}
	if err := track.WriteRTP(p); err != nil {
		return err
	}
	return nil
}

func (s *TrackSink) Close() error {
	for _, c := range s.closers {
		if err := c(); err != nil {
			return err
		}
	}
	return nil
}

var _ rtpio.RTPWriteCloser = (*TrackSink)(nil)
