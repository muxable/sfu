package server

import (
	"io"
	"log"
	"net"

	"github.com/muxable/sfu/pkg/av"
	"github.com/pion/webrtc/v3"
	"github.com/pkg/errors"
	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"
	"github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
	"go.uber.org/zap"
)

func RunRTMPServer(addr string, out chan *webrtc.TrackLocalStaticRTP) error {
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
					Handler: &RTMPHandler{trackCh: out},
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

	w io.WriteCloser
	enc *flv.Encoder

	trackCh chan *webrtc.TrackLocalStaticRTP
}

func (h *RTMPHandler) OnPublish(timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	if cmd.PublishingType != "live" {
		log.Printf("publish 1")
		return errors.New("unsupported publishing type")
	}
	if cmd.PublishingName == "" {
		log.Printf("publish 2")
		return errors.New("missing publishing name")
	}
	
	r, w := io.Pipe()
	videoCodec := webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264,
		ClockRate: 90000,
	}
	audioCodec := webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels: 2,
	}
	demux := av.NewRawDemuxer(r)
	decode := av.NewDecoder(demux)
	encode := av.NewEncoder(audioCodec, videoCodec, decode)
	mux := av.NewRTPMuxer(encode)
	go func() {
		if err := mux.CopyToTracks(cmd.PublishingName, h.trackCh); err != nil {
			log.Printf("muxer terminated %v", err)
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
		log.Printf("Failed to close: %+v", err)
	}
}