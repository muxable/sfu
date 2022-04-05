package tracks

import (
	"math/rand"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

var _ webrtc.TrackLocalContext = (*BondingTrack)(nil)

// BondingTrack represents a webrtc.TrackLocal that splits traffic across multiple interfaces.
type BondingTrack struct {
	sync.Mutex

	source *webrtc.TrackLocalStaticRTP

	params webrtc.RTPCodecParameters

	sinks []*webrtc.TrackLocalStaticRTP
}

func NewBondingTrack(track *webrtc.TrackLocalStaticRTP) *BondingTrack {
	return &BondingTrack{
		source: track,
	}
}

func (t *BondingTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	t.Lock()
	defer t.Unlock()

	if len(t.sinks) == 0 {
		params, err := t.source.Bind(t)
		if err != nil {
			return webrtc.RTPCodecParameters{}, err
		}
		t.params = params
	}

	// create a new track for this context.
	tl, err := webrtc.NewTrackLocalStaticRTP(t.params.RTPCodecCapability, t.source.ID(), t.source.StreamID(), webrtc.WithRTPStreamID(t.source.RID()))
	if err != nil {
		return webrtc.RTPCodecParameters{}, err
	}

	// add the track to the list of tracks.
	t.sinks = append(t.sinks, tl)

	return tl.Bind(ctx)
}

// Unbind should implement the teardown logic when the track is no longer needed. This happens
// because a track has been stopped.
func (t *BondingTrack) Unbind(ctx webrtc.TrackLocalContext) error {
	t.Lock()
	defer t.Unlock()

	// remove the track from the list of tracks.
	var matched *webrtc.TrackLocalStaticRTP
	for i, sink := range t.sinks {
		if sink.ID() == ctx.ID() {
			matched = sink
			t.sinks = append(t.sinks[:i], t.sinks[i+1:]...)
			break
		}
	}
	if matched == nil {
		return webrtc.ErrUnbindFailed
	}

	// unbind the track
	if err := matched.Unbind(ctx); err != nil {
		return err
	}

	// if there are no more tracks, unbind the parent track.
	if len(t.sinks) == 0 {
		return t.source.Unbind(t)
	}
	return nil
}

func (t *BondingTrack) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{
		webrtc.RTPCodecParameters{
			RTPCodecCapability: t.source.Codec(),
			PayloadType:        96, // this doesn't matter.
		},
	}
}

func (t *BondingTrack) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter {
	return nil // we don't need any header extensions.
}

func (t *BondingTrack) SSRC() webrtc.SSRC {
	return webrtc.SSRC(0) // also doesn't matter.
}

func (t *BondingTrack) WriteStream() webrtc.TrackLocalWriter {
	return t
}

func (t *BondingTrack) ID() string {
	return t.source.ID()
}

func (t *BondingTrack) RID() string {
	return t.source.RID()
}

func (t *BondingTrack) StreamID() string {
	return t.source.StreamID()
}

func (t *BondingTrack) Kind() webrtc.RTPCodecType {
	return t.source.Kind()
}

func (t *BondingTrack) RTCPReader() interceptor.RTCPReader {

}

func (t *BondingTrack) WriteRTP(header *rtp.Header, payload []byte) (int, error) {
	t.Lock()
	defer t.Unlock()

	p := &rtp.Packet{Header: *header, Payload: payload}
	sink := t.sinks[rand.Intn(len(t.sinks))]
	return p.MarshalSize(), sink.WriteRTP(p)
}

func (t *BondingTrack) Write(b []byte) (int, error) {
	t.Lock()
	defer t.Unlock()

	p := &rtp.Packet{}
	if err := p.Unmarshal(b); err != nil {
		return 0, err
	}

	sink := t.sinks[rand.Intn(len(t.sinks))]
	return p.MarshalSize(), sink.WriteRTP(p)
}

var _ webrtc.TrackLocal = (*BondingTrack)(nil)
