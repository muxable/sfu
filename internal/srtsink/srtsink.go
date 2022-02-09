package srtsink

import (
	"context"
	"sync"

	"github.com/haivision/srtgo"
	"github.com/muxable/rtptools/pkg/h265"
	"github.com/pion/rtp/codecs"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3/pkg/media/samplebuilder"
	"github.com/rs/zerolog/log"
)

type SRTSink struct {
	sync.Mutex

	videoSinks []*srtgo.SrtSocket
	audioSinks []*srtgo.SrtSocket
}

func NewSRTSink(in rtpio.RTPReader) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := &SRTSink{}

	videoSck := srtgo.NewSrtSocket("0.0.0.0", 7001, map[string]string{})
	defer videoSck.Close()
	videoSck.Listen(1)
	go func() {
		for ctx.Err() == nil {
			s, _, _ := videoSck.Accept()
			sink.Lock()
			sink.videoSinks = append(sink.videoSinks, s)
			sink.Unlock()
		}
	}()

	videoBuilder := samplebuilder.New(10, &h265.H265Packet{}, 90000)

	audioSck := srtgo.NewSrtSocket("0.0.0.0", 7002, map[string]string{})
	defer audioSck.Close()
	audioSck.Listen(1)
	go func() {
		for ctx.Err() == nil {
			s, _, _ := audioSck.Accept()
			sink.Lock()
			sink.audioSinks = append(sink.audioSinks, s)
			sink.Unlock()
		}
	}()

	audioBuilder := samplebuilder.New(10, &codecs.OpusPacket{}, 48000)

	for {
		p, err := in.ReadRTP()
		if err != nil {
			return err
		}
		switch p.PayloadType {
		case 106: // h265
			videoBuilder.Push(p)
			for {
				sample := videoBuilder.Pop()
				if sample == nil {
					break
				}

				sink.Lock()
				data := sample.Data
				for len(data) > 0 {
					chunk := len(data)
					if chunk > 1316 {
						chunk = 1316
					}
					for _, s := range sink.videoSinks {
						log.Printf("writing %x", data[:chunk])
						if _, err := s.Write(data[:chunk]); err != nil {
							log.Error().Err(err).Msg("error sending video packet")
						}
					}
					data = data[chunk:]
				}
				sink.Unlock()
			}
		case 111: // opus
			audioBuilder.Push(p)
			for {
				sample := audioBuilder.Pop()
				if sample == nil {
					break
				}

				sink.Lock()
				data := sample.Data
				for len(data) > 0 {
					chunk := len(data)
					if chunk > 1316 {
						chunk = 1316
					}
					for _, s := range sink.audioSinks {
						if _, err := s.Write(data[:chunk]); err != nil {
							log.Error().Err(err).Msg("error sending audio packet")
						}
					}
					data = data[chunk:]
				}
				sink.Unlock()
			}
		default:
			log.Warn().Uint8("PayloadType", p.PayloadType).Msg("unsupported payload type")
		}
	}
}
