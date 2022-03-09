package server

import (
	"fmt"
	"log"
	"strings"

	"github.com/haivision/srtgo"
	"github.com/muxable/sfu/pkg/av"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

func parseStreamID(sid string) (map[string]string, error) {
	const header = "#!::"

	if !strings.HasPrefix(sid, header) {
		return map[string]string{
			"r": sid,
			"m": "request",
		}, nil
	}

	items := map[string]string{"m": "request"}
	for _, item := range strings.Split(sid[len(header):], ",") {
		kv := strings.Split(item, "=")
		if len(kv) == 2 {
			return nil, fmt.Errorf("invalid stream id %s", sid)
		}
		items[kv[0]] = kv[1]
	}
	return items, nil
}

func RunSRTServer(host string, port uint16, trackHandler TrackHandler) error {
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
		items, err := parseStreamID(sid)
		if err != nil {
			return err
		}
		if items["r"] == "" {
			return fmt.Errorf("missing stream id %s", sid)
		}
		if items["m"] == "publish" {
			videoCodec := webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeH264,
				ClockRate: 90000,
			}
			audioCodec := webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeOpus,
				ClockRate: 48000,
				Channels:  2,
			}
			demux := av.NewRawDemuxer(conn)
			decode := av.NewDecoder(demux)
			encode := av.NewEncoder(audioCodec, videoCodec, decode)
			mux := av.NewRTPMuxer(encode)
			go func() {
				defer conn.Close()
				if err := CopyTracks(sid, trackHandler, mux); err != nil {
					log.Printf("muxer terminated %v", err)
				}
			}()
		}
	}
}
