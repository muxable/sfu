package av

/*
#cgo pkg-config: libavcodec libavformat
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include "rtp_mux.h"
*/
import "C"
import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/rtp"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/sdp"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

var (
	crtp = C.CString("rtp")
)

type RTPMuxContext struct {
	avformatctxs []*C.AVFormatContext
	Sink         rtpio.RTPWriteCloser
	packet       *rtp.Packet
}

func NewRTPMuxer(params []*AVCodecParameters) (*RTPMuxContext, error) {
	outputformat := C.av_guess_format(crtp, nil, nil)
	if outputformat == nil {
		return nil, errors.New("failed to find rtp output format")
	}

	buf := C.av_malloc(1200)
	if buf == nil {
		return nil, errors.New("failed to allocate buffer")
	}

	c := &RTPMuxContext{avformatctxs: make([]*C.AVFormatContext, len(params)), packet: &rtp.Packet{}}

	for i, param := range params {
		var avformatctx *C.AVFormatContext

		if averr := C.avformat_alloc_output_context2(&avformatctx, outputformat, nil, nil); averr < 0 {
			return nil, av_err("avformat_alloc_output_context2", averr)
		}

		avioctx := C.avio_alloc_context((*C.uchar)(buf), 1200, 1, pointer.Save(c), nil, (*[0]byte)(C.cgoWriteRTPPacketFunc), nil)
		if avioctx == nil {
			return nil, errors.New("failed to create avio context")
		}

		avioctx.max_packet_size = 1200

		avformatctx.pb = avioctx

		avformatstream := C.avformat_new_stream(avformatctx, nil)
		if avformatstream == nil {
			return nil, errors.New("failed to create rtp stream")
		}

		if averr := C.avcodec_parameters_copy(avformatstream.codecpar, param.codecpar); averr < 0 {
			return nil, av_err("avcodec_parameters_copy", averr)
		}

		var opts *C.AVDictionary
		if averr := C.av_dict_set_int(&opts, C.CString("payload_type"), C.int64_t(96+i), 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		if averr := C.av_dict_set(&opts, C.CString("strict"), C.CString("experimental"), 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		if averr := C.avformat_write_header(avformatctx, &opts); averr < 0 {
			return nil, av_err("avformat_write_header", averr)
		}

		c.avformatctxs[i] = avformatctx
	}

	return c, nil
}

//export goWriteRTPPacketFunc
func goWriteRTPPacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	c := pointer.Restore(opaque).(*RTPMuxContext)
	b := C.GoBytes(unsafe.Pointer(buf), bufsize)
	if err := c.packet.Unmarshal(b); err != nil {
		zap.L().Error("failed to unmarshal rtp packet", zap.Error(err))
		return -1
	}
	if c.packet.PayloadType == 72 {
		// ignore rtcp.
		return bufsize
	}
	if sink := c.Sink; sink != nil {
		if err := c.Sink.WriteRTP(c.packet); err != nil {
			zap.L().Error("failed to write rtp packet", zap.Error(err))
			return -1
		}
	}
	return bufsize
}

func (c *RTPMuxContext) WriteAVPacket(p *AVPacket) error {
	avformatctx := c.avformatctxs[p.packet.stream_index]
	// this can happen when draining.
	if avformatctx.nb_streams == 0 {
		return nil
	}
	p.packet.stream_index = 0
	stream := (*[1 << 30]*C.AVStream)(unsafe.Pointer(avformatctx.streams))[0]
	C.av_packet_rescale_ts(p.packet, p.timebase, stream.time_base)
	if averr := C.av_write_frame(avformatctx, p.packet); averr < 0 {
		return av_err("av_write_frame", averr)
	}
	return nil
}

func (c *RTPMuxContext) Close() error {
	// for _, avformatctx := range c.avformatctxs {
	// 	if averr := C.av_write_frame(avformatctx, nil); averr < 0 {
	// 		return av_err("av_write_frame", averr)
	// 	}

	// 	if averr := C.av_write_trailer(avformatctx); averr < 0 {
	// 		return av_err("av_write_trailer", averr)
	// 	}
	// }

	// close the sink
	if sink := c.Sink; sink != nil {
		if err := sink.Close(); err != nil {
			return err
		}
	}

	// for _, avformatctx := range c.avformatctxs {
	// 	C.avformat_free_context(avformatctx)
	// }

	return nil
}

func (c *RTPMuxContext) RTPCodecParameters() ([]*webrtc.RTPCodecParameters, error) {
	buf := C.av_mallocz(16384)
	if averr := C.av_sdp_create((**C.AVFormatContext)(unsafe.Pointer(&c.avformatctxs[0])), C.int(len(c.avformatctxs)), (*C.char)(buf), 16384); averr < 0 {
		return nil, av_err("av_sdp_create", averr)
	}

	s := sdp.SessionDescription{}
	if err := s.Unmarshal(C.GoString((*C.char)(buf))); err != nil {
		return nil, err
	}

	params := make([]*webrtc.RTPCodecParameters, len(s.MediaDescriptions))
	for i, desc := range s.MediaDescriptions {
		if len(desc.MediaName.Formats) != 1 {
			return nil, errors.New("unexpected number of formats")
		}
		pt, err := strconv.ParseUint(desc.MediaName.Formats[0], 10, 8)
		if err != nil {
			return nil, err
		}
		rtpmap, ok := desc.Attribute("rtpmap")
		if !ok {
			// uh oh, unsupported codec?
			// TODO: handle this properly, we should probably reject the input.
			continue
		}
		parts := strings.Split(rtpmap, " ")
		if len(parts) != 2 {
			return nil, errors.New("unexpected number of parts")
		}
		if parts[0] != fmt.Sprint(pt) {
			return nil, errors.New("unexpected payload type")
		}
		data := strings.Split(parts[1], "/")

		mime := fmt.Sprintf("%s/%s", desc.MediaName.Media, data[0])
		clockRate, err := strconv.ParseUint(data[1], 10, 32)
		if err != nil {
			return nil, err
		}
		var channels uint64
		if len(data) > 2 {
			channels, err = strconv.ParseUint(data[2], 10, 16)
			if err != nil {
				return nil, err
			}
		}
		fmtp, ok := desc.Attribute("fmtp")
		if ok {
			fmtp = fmtp[len(fmt.Sprint(pt))+1:]
		}

		params[i] = &webrtc.RTPCodecParameters{
			PayloadType: webrtc.PayloadType(pt),
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    mime,
				ClockRate:   uint32(clockRate),
				Channels:    uint16(channels),
				SDPFmtpLine: fmtp,
			},
		}
	}
	return params, nil
}
