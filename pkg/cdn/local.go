package cdn

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

type LocalCDN struct {
	sync.Mutex

	tracks      map[string][]*CDNTrackLocalStaticRTP
	subscribers map[string][]*StreamSubscription

	subscriptions []*TrackSubscription
}

// a local cdn node
func NewLocalCDN() *LocalCDN {
	return &LocalCDN{
		tracks:      make(map[string][]*CDNTrackLocalStaticRTP),
		subscribers: make(map[string][]*StreamSubscription),
	}
}

type TrackSubscription struct {
	*StreamSubscription
	*CDNTrackLocalStaticRTP
	onClose func()
}

func (c *TrackSubscription) OnClose(cb func()) {
	c.onClose = cb
}

type StreamSubscription struct {
	streamID string
	trackCh  chan *TrackSubscription
}

func (s *StreamSubscription) NextTrack() *TrackSubscription {
	return <-s.trackCh
}

func (c *LocalCDN) Subscribe(ctx context.Context, streamID string) *StreamSubscription {
	c.Lock()
	defer c.Unlock()

	zap.L().Debug("subscribing to stream", zap.String("id", streamID))

	subscription := &StreamSubscription{
		streamID: streamID,
		trackCh:  make(chan *TrackSubscription, len(c.tracks[streamID])),
	}

	c.subscribers[streamID] = append(c.subscribers[streamID], subscription)

	// create a track subscription for each track in the stream
	for _, track := range c.tracks[streamID] {
		ts := &TrackSubscription{
			StreamSubscription:     subscription,
			CDNTrackLocalStaticRTP: track,
		}
		subscription.trackCh <- ts
		c.subscriptions = append(c.subscriptions, ts)
	}

	// create a cleanup handler
	go func() {
		<-ctx.Done()

		c.Lock()
		defer c.Unlock()

		// remove the subscription
		cleaned := make([]*TrackSubscription, 0, len(c.subscriptions))
		for _, sub := range c.subscriptions {
			if sub.StreamSubscription == subscription {
				sub.onClose()
			} else {
				cleaned = append(cleaned, sub)
			}
		}
		c.subscriptions = cleaned
	}()

	return subscription
}

func (c *LocalCDN) Get(streamID string) []*CDNTrackLocalStaticRTP {
	c.Lock()
	defer c.Unlock()

	return c.tracks[streamID]
}

func (c *LocalCDN) Publish(tl *CDNTrackLocalStaticRTP) func() {
	c.Lock()
	defer c.Unlock()

	zap.L().Debug("publishing track", zap.String("id", tl.ID()), zap.String("kind", tl.Kind().String()), zap.String("streamID", tl.StreamID()))

	c.tracks[tl.StreamID()] = append(c.tracks[tl.StreamID()], tl)

	for _, sub := range c.subscribers[tl.StreamID()] {
		ts := &TrackSubscription{
			StreamSubscription:     sub,
			CDNTrackLocalStaticRTP: tl,
		}
		sub.trackCh <- ts
		c.subscriptions = append(c.subscriptions, ts)
	}

	return func() {
		cleanedSubscriptions := make([]*TrackSubscription, 0, len(c.subscriptions))
		for _, sub := range c.subscriptions {
			if sub.CDNTrackLocalStaticRTP == tl {
				sub.onClose()
			} else {
				cleanedSubscriptions = append(cleanedSubscriptions, sub)
			}
		}
		c.subscriptions = cleanedSubscriptions

		// remove it from the track map too.
		cleanedTracks := make([]*CDNTrackLocalStaticRTP, 0, len(c.tracks[tl.StreamID()]))
		for _, track := range c.tracks[tl.StreamID()] {
			if track != tl {
				cleanedTracks = append(cleanedTracks, track)
			}
		}
		c.tracks[tl.StreamID()] = cleanedTracks
	}
}
