package cdn

import (
	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
)

type Binding struct {
	rtpio.RTPWriter
	webrtc.PayloadType
}

type CDNTrackLocalStaticRTP struct {
	*webrtc.TrackLocalStaticRTP

	bindings map[string]*Binding
}

func NewCDNTrackLocalStaticRTP(t *webrtc.TrackLocalStaticRTP) *CDNTrackLocalStaticRTP {
	return &CDNTrackLocalStaticRTP{
		TrackLocalStaticRTP: t,
		bindings:            make(map[string]*Binding),
	}
}

func (t *CDNTrackLocalStaticRTP) AddListener(pt webrtc.PayloadType, w rtpio.RTPWriter) string {
	id := uuid.NewString()
	t.bindings[id] = &Binding{w, pt}
	return id
}

func (t *CDNTrackLocalStaticRTP) RemoveListener(id string) {
	delete(t.bindings, id)
}

func (t *CDNTrackLocalStaticRTP) WriteRTP(p *rtp.Packet) error {
	for _, binding := range t.bindings {
		if binding.PayloadType != 0 {
			p.PayloadType = uint8(binding.PayloadType)
		}
		if err := binding.WriteRTP(p); err != nil {
			// zap.L().Error("failed to write RTP packet", zap.Error(err))
		}
	}
	if err := t.TrackLocalStaticRTP.WriteRTP(p); err != nil {
		// zap.L().Error("failed to write RTP packet", zap.Error(err))
	}
	return nil
}

func (t *CDNTrackLocalStaticRTP) Write(b []byte) (n int, err error) {
	p := &rtp.Packet{}
	if err = p.Unmarshal(b); err != nil {
		return 0, err
	}
	return len(b), t.WriteRTP(p)
}
