package server

import (
	"github.com/haivision/srtgo"
	"github.com/muxable/sfu/internal/mpegts"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

func RunSRTServer(host string, port uint16, out chan *webrtc.TrackLocalStaticRTP) error {
	zap.L().Info("listening for SRT", zap.String("host", host), zap.Uint16("port", port))

	options := map[string]string{
		"transtype": "live",
	}

	sck := srtgo.NewSrtSocket(host, port, options)
	defer sck.Close()
	if err := sck.Listen(1); err != nil {
		return err
	}
	for {
		conn, _, err := sck.Accept()
		if err != nil {
			return err
		}
		sid, err := conn.GetSockOptString(srtgo.SRTO_STREAMID)
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			d, err := mpegts.NewDemuxer(conn)
			if err != nil {
				zap.L().Error("failed to create demuxer", zap.Error(err))
				return
			}

			if err := d.CopyToTracks(sid, out); err != nil {
				zap.L().Error("failed to copy to tracks", zap.Error(err))
				return
			}
		}()
	}
}
