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
		decoders, err := demux.NewDecoders()
		if err != nil {
			zap.L().Error("failed to create decoders", zap.Error(err))
			return
		}
		configs, err := decoders.MapEncoderConfigurations(&av.EncoderConfiguration{
			Codec: h.audioCodec,
		}, &av.EncoderConfiguration{
			Codec: h.videoCodec,
		})
		if err != nil {
			zap.L().Error("failed to create encoder configurations", zap.Error(err))
			return
		}
		encoders, err := decoders.NewEncoders(configs)
		if err != nil {
			zap.L().Error("failed to create encoders", zap.Error(err))
			return
		}
		mux, err := encoders.NewRTPMuxer()
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
		mux.Sink = trackSink

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
