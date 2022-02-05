package srtsink

/*
#cgo CFLAGS: -I/usr/local/lib/x86_64-linux-gnu
#cgo pkg-config: gstreamer-1.0
#include "gstreamer.h"
*/
import "C"
import (
	"net"
	"unsafe"

	"github.com/pion/rtp/codecs"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/rs/zerolog/log"
)

type SRTSink struct {
	gstElement *C.GstElement
}

func init() {
	go C.g_main_loop_run(C.g_main_loop_new(nil, C.int(0)))
	C.gst_init(nil, nil)
}

func NewSRTSink(in rtpio.RTPReader) error {
	p := `
		udpsrc port=7010 do-timestamp=true caps="application/x-rtp" ! rtph265depay ! h265parse ! queue ! mux.
		udpsrc port=7020 do-timestamp=true caps="application/x-rtp" ! rtpopusdepay ! opusparse ! queue ! mux.
		mpegtsmux name=mux ! srtsink mode=listener localaddress=0.0.0.0`
	cp := C.CString(p)
	defer C.free(unsafe.Pointer(cp))
	C.gstreamer_start(cp, nil)
	videoConn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4zero, Port: 7010})
	if err != nil {
		return err
	}
	audioConn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4zero, Port: 7020})
	if err != nil {
		return err
	}
	videoDepacketizer := &codecs.H265Packet{}
	audioDepacketizer := &codecs.OpusPacket{}
	for {
		p, err := in.ReadRTP()
		if err != nil {
			return err
		}
		pbuf, err := p.Marshal()
		if err != nil {
			return err
		}
		switch p.PayloadType {
		case 106: // h265
			buf, err := videoDepacketizer.Unmarshal(pbuf)
			if err != nil {
				return err
			}
			if _, err := videoConn.Write(buf); err != nil {
				return err
			}
		case 111: // opus
			buf, err := audioDepacketizer.Unmarshal(pbuf)
			if err != nil {
				return err
			}
			if _, err := audioConn.Write(buf); err != nil {
				return err
			}
		default:
			log.Warn().Uint8("PayloadType", p.PayloadType).Msg("unsupported payload type")
		}
	}
	return nil
}
