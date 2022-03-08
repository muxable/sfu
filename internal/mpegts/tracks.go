package mpegts

import (
	"fmt"

	"github.com/pion/webrtc/v3"
)

func (c *DemuxContext) CopyToTracks(sid string, trackCh chan *webrtc.TrackLocalStaticRTP) error {
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
		trackCh <- track
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