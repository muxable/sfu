package av

import (
	"errors"

	"github.com/pion/webrtc/v3"
)

// this file contains convenience functions for building the demux -> decode -> encode -> mux pipelines.

type DecodeContexts []*DecodeContext
type EncodeContexts []*EncodeContext

func (c *DemuxContext) NewDecoders() (DecodeContexts, error) {
	streams := c.Streams()
	decoders := make([]*DecodeContext, len(streams))
	for i, stream := range streams {
		decoder, err := NewDecoder(c, stream)
		if err != nil {
			return nil, err
		}
		decoders[i] = decoder
		c.Sinks = append(c.Sinks, &IndexedSink{Index: 0, AVPacketWriteCloser: decoder})
	}
	return decoders, nil
}

func (c *DeviceContext) NewDecoders() (DecodeContexts, error) {
	streams := c.Streams()
	decoders := make([]*DecodeContext, len(streams))
	for i, stream := range streams {
		decoder, err := NewDecoder(c, stream)
		if err != nil {
			return nil, err
		}
		decoders[i] = decoder
		c.Sinks = append(c.Sinks, &IndexedSink{Index: 0, AVPacketWriteCloser: decoder})
	}
	return decoders, nil
}

func (c DecodeContexts) MapEncoderConfigurations(audioCodec, videoCodec webrtc.RTPCodecCapability) ([]*EncoderConfiguration, error) {
	configs := make([]*EncoderConfiguration, len(c))
	for i, decoder := range c {
		switch decoder.RTPCodecType {
		case webrtc.RTPCodecTypeAudio:
			configs[i] = &EncoderConfiguration{Codec: audioCodec}
		case webrtc.RTPCodecTypeVideo:
			configs[i] = &EncoderConfiguration{Codec: videoCodec}
		default:
			return nil, errors.New("unsupported media type")
		}
	}
	return configs, nil
}

func (c DecodeContexts) NewEncoders(configs []*EncoderConfiguration) (EncodeContexts, error) {
	encoders := make([]*EncodeContext, len(c))
	for i, decoder := range c {
		encoder, err := decoder.NewEncoder(configs[i])
		if err != nil {
			return nil, err
		}
		decoder.Sink = encoder
		encoders[i] = encoder
	}
	return encoders, nil
}

func (c *DecodeContext) NewEncoder(configuration *EncoderConfiguration) (*EncodeContext, error) {
	enc, err := NewEncoder(c, configuration)
	if err != nil {
		return nil, err
	}

	// check if we need to resample.
	switch c.RTPCodecType {
	case webrtc.RTPCodecTypeAudio:
	if c.decoderctx.sample_rate != enc.encoderctx.sample_rate {
		resampler, err := NewResampler(c, enc)
		if err != nil {
			return nil, err
		}
		c.Sink = resampler
		resampler.Sink = enc
	} else {
		c.Sink = enc
	}
	case webrtc.RTPCodecTypeVideo:
		c.Sink = enc
	}

	return enc, nil
}

func (c EncodeContexts) NewRTPMuxer() (*RTPMuxContext, error) {
	params := make([]*AVCodecParameters, len(c))
	for i, encoder := range c {
		params[i] = NewAVCodecParametersFromEncoder(encoder)
	}

	mux, err := NewRTPMuxer(params)
	if err != nil {
		return nil, err
	}

	for i, encoder := range c {
		encoder.Sink = &IndexedSink{Index: i, AVPacketWriteCloser: mux}
	}
	return mux, nil
}

func (c EncodeContexts) NewRawMuxer(format string) (*RawMuxContext, error) {
	params := make([]*AVCodecParameters, len(c))
	for i, encoder := range c {
		params[i] = NewAVCodecParametersFromEncoder(encoder)
	}

	mux, err := NewRawMuxer(format, params)
	if err != nil {
		return nil, err
	}

	for i, encoder := range c {
		encoder.Sink = &IndexedSink{Index: i, AVPacketWriteCloser: mux}
	}
	return mux, nil
}