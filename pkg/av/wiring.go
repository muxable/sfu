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

func (c DecodeContexts) MapEncoderConfigurations(audio, video *EncoderConfiguration) ([]*EncoderConfiguration, error) {
	configs := make([]*EncoderConfiguration, len(c))
	for i, decoder := range c {
		switch decoder.RTPCodecType {
		case webrtc.RTPCodecTypeAudio:
			configs[i] = audio
		case webrtc.RTPCodecTypeVideo:
			configs[i] = video
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
		encoders[i] = encoder
	}
	return encoders, nil
}

func (c *DecodeContext) NewEncoder(configuration *EncoderConfiguration) (*EncodeContext, error) {
	// copy the parameters from the decoder if they're not overwritten.
	if configuration.Width == 0 {
		configuration.Width = uint32(c.decoderctx.width)
	}
	if configuration.Height == 0 {
		configuration.Height = uint32(c.decoderctx.height)
	}
	if configuration.SampleAspectRatioNumerator == 0 || configuration.SampleAspectRatioDenominator == 0 {
		configuration.SampleAspectRatioNumerator = uint32(c.decoderctx.sample_aspect_ratio.num)
		configuration.SampleAspectRatioDenominator = uint32(c.decoderctx.sample_aspect_ratio.den)
	}
	if configuration.FrameRateNumerator == 0 || configuration.FrameRateDenominator == 0 {
		configuration.FrameRateNumerator = uint32(c.decoderctx.framerate.num)
		configuration.FrameRateDenominator = uint32(c.decoderctx.framerate.den)
	}
	configuration.TimeBaseNumerator = uint32(c.decoderctx.time_base.num)
	configuration.TimeBaseDenominator = uint32(c.decoderctx.time_base.den)

	enc, err := NewEncoder(configuration)
	if err != nil {
		return nil, err
	}

	// check if we need to resample.
	filter, err := NewFilter(c, enc)
	if err != nil {
		return nil, err
	}

	c.Sink = filter
	filter.Sink = enc

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