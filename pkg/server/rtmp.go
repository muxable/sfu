package server

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/muxable/ingress/internal/packetizer"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pkg/errors"
	flvtag "github.com/yutopp/go-flv/tag"
	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
	"go.uber.org/zap"
)

type RTMPServer struct {
	*rtmp.Server

	trackCh chan webrtc.TrackLocal
}

func NewRTMPServer() *RTMPServer {
	trackCh := make(chan webrtc.TrackLocal)
	return &RTMPServer{
		Server: rtmp.NewServer(&rtmp.ServerConfig{
			OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
				return conn, &rtmp.ConnConfig{
					Handler: &Handler{trackCh: trackCh},
					ControlState: rtmp.StreamControlStateConfig{
						DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
					},
				}
			},
		}),
		trackCh: trackCh,
	}
}

func (s *RTMPServer) AcceptTrackLocal() (webrtc.TrackLocal, error) {
	track, ok := <-s.trackCh
	if !ok {
		return nil, io.EOF
	}
	return track, nil
}

var _ TrackProducer = (*RTMPServer)(nil)
var _ TCPServer = (*RTMPServer)(nil)

type OutputTrack struct {
	track *webrtc.TrackLocalStaticRTP
	packetizer rtp.Packetizer
}

func (t *OutputTrack) Write(buf []byte, rtpts uint32) error {
	for _, p := range t.packetizer.Packetize(buf, rtpts) {
		if err := t.track.WriteRTP(p); err != nil {
			return err
		}
	}
	return nil
}

type Handler struct {
	rtmp.DefaultHandler
	sync.Mutex

	cname *string

	audioTracks map[flvtag.SoundFormat]*OutputTrack
	videoTracks map[flvtag.CodecID]*OutputTrack

	trackCh chan webrtc.TrackLocal  // shared across all handlers.
}

func (h *Handler) OnServe(conn *rtmp.Conn) {
}

func (h *Handler) OnConnect(timestamp uint32, cmd *rtmpmsg.NetConnectionConnect) error {
	log.Printf("OnConnect: %#v", cmd)
	return nil
}

func (h *Handler) OnCreateStream(timestamp uint32, cmd *rtmpmsg.NetConnectionCreateStream) error {
	log.Printf("OnCreateStream: %#v", cmd)
	return nil
}

func (h *Handler) OnPublish(_ *rtmp.StreamContext, timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	h.Lock()
	defer h.Unlock()
	
	log.Printf("OnPublish: %#v", cmd)
	if cmd.PublishingType != "live" {
		return errors.New("unsupported publishing type")
	}

	// record the stream name as the cname.
	h.cname = &cmd.PublishingName

	return nil
}

func (h *Handler) OnSetDataFrame(timestamp uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
	zap.L().Info("rtmp received data frame")
	return nil
}

func toClockRate(data flvtag.AudioData) uint32 {
	switch data.SoundRate {
	case flvtag.SoundRate5_5kHz:
		return 5500
	case flvtag.SoundRate11kHz:
		return 11000
	case flvtag.SoundRate22kHz:
		return 22000
	case flvtag.SoundRate44kHz:
		return 44000
	}
	return 0
}

func toAudioCodec(data flvtag.AudioData) (*webrtc.RTPCodecCapability, error) {
	switch data.SoundFormat {
		case flvtag.SoundFormatAAC:
			return &webrtc.RTPCodecCapability{
				MimeType: "audio/aac",
				ClockRate: toClockRate(data),
				Channels: uint16(data.SoundType) + 1,
			}, nil
		case flvtag.SoundFormatMP3:
			return &webrtc.RTPCodecCapability{
				MimeType: "audio/mpeg",
				ClockRate: toClockRate(data),
				Channels: uint16(data.SoundType) + 1,
			}, nil
		case flvtag.SoundFormatG711ALawLogarithmicPCM:
			return &webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypePCMA,
				ClockRate: toClockRate(data),
				Channels: uint16(data.SoundType) + 1,
			}, nil
		case flvtag.SoundFormatG711muLawLogarithmicPCM:
			return &webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypePCMU,
				ClockRate: toClockRate(data),
				Channels: uint16(data.SoundType) + 1,
			}, nil
	}
	return nil, fmt.Errorf("unsupported audio format: %d", data.SoundFormat)
}

func (h *Handler) OnAudio(timestamp uint32, payload io.Reader) error {
	h.Lock()
	defer h.Unlock()

	var audio flvtag.AudioData
	if err := flvtag.DecodeAudioData(payload, &audio); err != nil {
		return err
	}

	flvBody := new(bytes.Buffer)
	if _, err := io.Copy(flvBody, audio.Data); err != nil {
		return err
	}

	track, ok := h.audioTracks[audio.SoundFormat]
	if !ok {
		codec, err := toAudioCodec(audio)
		if err != nil {
			return err
		}
		tl, err := webrtc.NewTrackLocalStaticRTP(*codec, fmt.Sprintf("%s-rtmp-audio-%d", *h.cname, audio.SoundFormat), *h.cname)
		if err != nil {
			return err
		}
		track = &OutputTrack{
			track: tl,
			packetizer: packetizer.NewTSPacketizer(1200, &codecs.H264Payloader{}, rtp.NewRandomSequencer()),
		}
		h.audioTracks[audio.SoundFormat] = track
	}

	return track.Write(flvBody.Bytes(), timestamp * 90)
}

func toVideoCodec(data flvtag.VideoData) (*webrtc.RTPCodecCapability, error) {
	switch data.CodecID {
	case flvtag.CodecIDAVC:
		return &webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, nil
	}
	return nil, errors.Errorf("unsupported codec: %+v", data.CodecID)
}

func (h *Handler) OnVideo(timestamp uint32, payload io.Reader) error {
	h.Lock()
	defer h.Unlock()

	if h.cname == nil {
		return errors.New("no stream name")
	}
	
	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
		return err
	}

	flvBody := new(bytes.Buffer)
	if _, err := io.Copy(flvBody, video.Data); err != nil {
		return err
	}

	track, ok := h.videoTracks[video.CodecID]
	if !ok {
		codec, err := toVideoCodec(video)
		if err != nil {
			return err
		}
		tl, err := webrtc.NewTrackLocalStaticRTP(*codec, fmt.Sprintf("%s-rtmp-video-%d", *h.cname, video.CodecID), *h.cname)
		if err != nil {
			return err
		}
		track = &OutputTrack{
			track: tl,
			packetizer: packetizer.NewTSPacketizer(1200, &codecs.H264Payloader{}, rtp.NewRandomSequencer()),
		}
		h.videoTracks[video.CodecID] = track
	}

	// https://stackoverflow.com/a/22582945/86433
	dts := timestamp * 90
	pts := (uint32(video.CompositionTime) * 90) + dts

	return track.Write(flvBody.Bytes(), pts)
}

func (h *Handler) OnClose() {
}
