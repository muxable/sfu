package server

import (
	"io"
	"net"

	av "github.com/muxable/sfu/pkg/av"
	"github.com/pion/webrtc/v3"
	"github.com/pkg/errors"
	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"
	"github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
	"go.uber.org/zap"
)

func RunRTMPServer(addr string, trackHandler TrackHandler, videoCodec, audioCodec webrtc.RTPCodecCapability) error {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return err
	}

	zap.L().Info("listening for RTMP", zap.String("addr", addr))

	s := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			return conn, &rtmp.ConnConfig{
				Handler: &RTMPHandler{trackHandler: trackHandler, videoCodec: videoCodec, audioCodec: audioCodec},
				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},
			}
		},
	})
	return s.Serve(conn)
}

type RTMPHandler struct {
	rtmp.DefaultHandler

	w   io.WriteCloser
	enc *flv.Encoder

	videoCodec, audioCodec webrtc.RTPCodecCapability

	trackHandler TrackHandler
}

func (h *RTMPHandler) OnPublish(timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	if cmd.PublishingType != "live" {
		return errors.New("unsupported publishing type")
	}
	if cmd.PublishingName == "" {
		return errors.New("missing publishing name")
	}

	r, w := io.Pipe()
	go func() {

		// construct the elements
		demux, err := av.NewRawDemuxer(r)
		if err != nil {
			zap.L().Error("failed to create demuxer", zap.Error(err))
			return
		}
		streams := demux.Streams()
		decoders := make([]*av.DecodeContext, len(streams))
		converters := make([]*av.ResampleContext, len(streams))
		encoders := make([]*av.EncodeContext, len(streams))
		parameters := make([]*av.AVCodecParameters, len(streams))
		for i, stream := range demux.Streams() {
			decoder, err := av.NewDecoder(demux, stream)
			if err != nil {
				zap.L().Error("failed to create decoder", zap.Error(err))
				return
			}
			switch stream.AVMediaType() {
			case av.AVMediaTypeVideo:
				encoder, err := av.NewEncoder(decoder, &av.EncoderConfiguration{Codec: h.videoCodec})
				if err != nil {
					zap.L().Error("failed to create encoder", zap.Error(err))
					return
				}
				decoders[i] = decoder
				encoders[i] = encoder
				parameters[i] = av.NewAVCodecParametersFromEncoder(encoder)
			case av.AVMediaTypeAudio:
				encoder, err := av.NewEncoder(decoder, &av.EncoderConfiguration{Codec: h.audioCodec})
				if err != nil {
					zap.L().Error("failed to create encoder", zap.Error(err))
					return
				}
				resampler, err := av.NewResampler(decoder, encoder)
				if err != nil {
					zap.L().Error("failed to create resampler", zap.Error(err))
					return
				}
				decoders[i] = decoder
				converters[i] = resampler
				encoders[i] = encoder
				parameters[i] = av.NewAVCodecParametersFromEncoder(encoder)
			default:
				zap.L().Error("unsupported media type", zap.Int("media_type", int(stream.AVMediaType())))
			}
		}
		mux, err := av.NewRTPMuxer(parameters)
		if err != nil {
			zap.L().Error("failed to create muxer", zap.Error(err))
			return
		}
		params, err := mux.RTPCodecParameters()
		if err != nil {
			zap.L().Error("failed to get codec parameters", zap.Error(err))
			return
		}
		trackSink, err := NewTrackSink(params, cmd.PublishingName, h.trackHandler)
		if err != nil {
			zap.L().Error("failed to create sink", zap.Error(err))
			return
		}

		// wire them together
		for i := 0; i < len(streams); i++ {
			demux.Sinks = append(demux.Sinks, &av.IndexedSink{AVPacketWriteCloser: decoders[i], Index: 0})
			if resampler := converters[i]; resampler != nil {
				decoders[i].Sink = resampler
				resampler.Sink = encoders[i]
			} else {
				decoders[i].Sink = encoders[i]
			}
			encoders[i].Sink = &av.IndexedSink{AVPacketWriteCloser: mux, Index: i}
		}
		mux.Sink = trackSink

		// TODO: validate the construction

		// start the pipeline
		if err := demux.Run(); err != nil {
			if err != io.EOF {
				zap.L().Error("failed to run pipeline", zap.Error(err))
			}
			if err := demux.Close(); err != nil {
				zap.L().Error("failed to close pipeline", zap.Error(err))
			}
			return
		}
	}()

	enc, err := flv.NewEncoder(w, flv.FlagsAudio|flv.FlagsVideo)
	if err != nil {
		return err
	}
	h.enc = enc
	h.w = w

	return nil
}

func (h *RTMPHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	var audio flvtag.AudioData
	if err := flvtag.DecodeAudioData(payload, &audio); err != nil {
		return err
	}
	if h.enc == nil {
		return errors.New("not publishing")
	}
	return h.enc.Encode(&flvtag.FlvTag{
		TagType:   flvtag.TagTypeAudio,
		Timestamp: timestamp,
		Data:      &audio,
	})
}

func (h *RTMPHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
		return err
	}
	if h.enc == nil {
		return errors.New("not publishing")
	}
	return h.enc.Encode(&flvtag.FlvTag{
		TagType:   flvtag.TagTypeVideo,
		Timestamp: timestamp,
		Data:      &video,
	})
}

func (h *RTMPHandler) OnClose() {
	if err := h.w.Close(); err != nil {
		zap.L().Error("failed to close flv writer", zap.Error(err))
	}
}
