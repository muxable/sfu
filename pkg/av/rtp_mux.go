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
	"io"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/rtp"
	"github.com/pion/sdp"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

var (
	crtp = C.CString("rtp")
)

type result struct {
	p *rtp.Packet
	e error
}

type RTPMuxContext struct {
	avformatctxs []*C.AVFormatContext
	packet       *AVPacket
	encoder      *EncodeContext
	pch          chan *result
}

func NewRTPMuxer(encoder *EncodeContext) *RTPMuxContext {
	c := &RTPMuxContext{
		packet:  NewAVPacket(),
		encoder: encoder,
		pch:     make(chan *result, 8),  // we need a small buffer here to avoid blocking initialization.
	}
	return c
}

//export goWriteRTPPacketFunc
func goWriteRTPPacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	m := pointer.Restore(opaque).(*RTPMuxContext)
	b := C.GoBytes(unsafe.Pointer(buf), bufsize)
	p := &rtp.Packet{}
	if err := p.Unmarshal(b); err != nil {
		zap.L().Error("failed to unmarshal rtp packet", zap.Error(err))
		return C.int(-1)
	}
	m.pch <- &result{p: p}
	return bufsize
}

func (c *RTPMuxContext) Initialize() error {
	if err := c.encoder.init(); err != nil {
		return err
	}

	outputformat := C.av_guess_format(crtp, nil, nil)
	if outputformat == nil {
		return errors.New("failed to find rtp output format")
	}

	buf := C.av_malloc(1200)
	if buf == nil {
		return errors.New("failed to allocate buffer")
	}

	var wg sync.WaitGroup

	for i := 0; i < c.encoder.decoder.Len(); i++ {
		var avformatctx *C.AVFormatContext

		if averr := C.avformat_alloc_output_context2(&avformatctx, outputformat, nil, nil); averr < 0 {
			return av_err("avformat_alloc_output_context2", averr)
		}

		avioctx := C.avio_alloc_context((*C.uchar)(buf), 1200, 1, pointer.Save(c), nil, (*[0]byte)(C.cgoWriteRTPPacketFunc), nil)
		if avioctx == nil {
			return errors.New("failed to create avio context")
		}

		avioctx.max_packet_size = 1200

		avformatctx.pb = avioctx

		avformatstream := C.avformat_new_stream(avformatctx, nil)
		if avformatstream == nil {
			return errors.New("failed to create rtp stream")
		}

		if averr := C.avcodec_parameters_from_context(avformatstream.codecpar, c.encoder.encoderctxs[i]); averr < 0 {
			return av_err("avcodec_parameters_from_context", averr)
		}

		var opts *C.AVDictionary
		if averr := C.av_dict_set_int(&opts, C.CString("payload_type"), C.int64_t(96+i), 0); averr < 0 {
			return av_err("av_dict_set_int", averr)
		}
		if averr := C.avformat_write_header(avformatctx, &opts); averr < 0 {
			return av_err("avformat_write_header", averr)
		}

		c.avformatctxs = append(c.avformatctxs, avformatctx)

		wg.Add(1)
		go func(i int) {
			for {
				if err := c.encoder.ReadAVPacket(i, c.packet); err != nil {
					if err != io.EOF {
						c.pch <- &result{e: err}
					}
					break
				}
				// stream index is always zero because there's n contexts, one stream each.
				c.packet.packet.stream_index = 0
				if averr := C.av_write_frame(c.avformatctxs[i], c.packet.packet); averr < 0 {
					c.pch <- &result{e: av_err("av_write_frame", averr)}
					break
				}
			}
			if averr := C.av_write_trailer(c.avformatctxs[i]); averr < 0 {
				c.pch <- &result{e: av_err("av_write_trailer", averr)}
			}
			wg.Done()
		}(i)
	}
	
	// cleanup thread
	go func() {
		wg.Wait()
		close(c.pch)
	}()

	return nil
}

func (c *RTPMuxContext) ReadRTP() (*rtp.Packet, error) {
	r, ok := <-c.pch
	if !ok {
		return nil, io.EOF
	}
	return r.p, r.e
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
				SDPFmtpLine: fmt.Sprintf("%s; profile-level-id=640032", fmtp),
			},
		}
	}
	return params, nil
}
