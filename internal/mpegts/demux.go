package mpegts

/*
#cgo pkg-config: libavformat libavcodec
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include "demux.h"
*/
import "C"
import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

var (
	crtp              = C.CString("rtp")
	ch264_mp4toannexb = C.CString("h264_mp4toannexb")
	chevc_mp4toannexb = C.CString("hevc_mp4toannexb")
)

type result struct {
	p *rtp.Packet
	e error
}

type DemuxContext struct {
	r               io.Reader
	inAVFormatCtx   *C.AVFormatContext
	outAVBSFCtxs    []*C.AVBSFContext
	outAVFormatCtxs []*C.AVFormatContext
	pch             chan *result
	streamMapping   map[int]int
}

func NewDemuxer(r io.Reader) (*DemuxContext, error) {
	c := &DemuxContext{r: r, pch: make(chan *result), streamMapping: make(map[int]int)}
	// we want initialization to be synchronous (unlike the transcoder) because initialization tells us what streams are available
	if err := c.init(); err != nil {
		return nil, err
	}
	go func() {
		defer close(c.pch)
		defer func() {
			for _, ctx := range c.outAVBSFCtxs {
				C.av_bsf_free(&ctx)
			}
		}()

		pkt := C.av_packet_alloc()
		if pkt == nil {
			c.pch <- &result{e: errors.New("failed to allocate packet")}
			return
		}
		defer C.av_packet_free(&pkt)

		defer func() {
			for _, ctx := range c.outAVFormatCtxs {
				if averr := C.av_write_trailer(ctx); averr < 0 {
					c.pch <- &result{e: av_err("av_write_trailer", averr)}
				}
			}
		}()

		for {
			if averr := C.av_read_frame(c.inAVFormatCtx, pkt); averr < 0 {
				c.pch <- &result{e: av_err("av_read_frame", averr)}
				return
			}

			instream := ((*[1 << 30]*C.AVStream)(unsafe.Pointer(c.inAVFormatCtx.streams)))[pkt.stream_index]
			index, ok := c.streamMapping[int(pkt.stream_index)]
			if !ok {
				continue
			}

			outavformatctx := c.outAVFormatCtxs[index]

			pkt.stream_index = C.int(0)
			outstream := ((*[1 << 30]*C.AVStream)(unsafe.Pointer(outavformatctx.streams)))[0]

			C.av_packet_rescale_ts(pkt, instream.time_base, outstream.time_base)
			pkt.pos = -1

			bsf := c.outAVBSFCtxs[index]
			if averr := C.av_bsf_send_packet(bsf, pkt); averr < 0 {
				c.pch <- &result{e: av_err("av_bsf_send_packet", averr)}
				return
			}

			for {
				if averr := C.av_bsf_receive_packet(bsf, pkt); averr < 0 {
					if averr == -C.EAGAIN {
						break
					}
					c.pch <- &result{e: av_err("av_bsf_receive_packet", averr)}
					return
				}

				if averr := C.av_write_frame(outavformatctx, pkt); averr < 0 {
					c.pch <- &result{e: av_err("av_interleaved_write_frame", averr)}
					return
				}
			}
		}
	}()
	return c, nil
}

//export goReadPacketFunc
func goReadPacketFunc(opaque unsafe.Pointer, cbuf *C.uint8_t, bufsize C.int) C.int {
	d := pointer.Restore(opaque).(*DemuxContext)
	buf := make([]byte, int(bufsize))
	n, err := d.r.Read(buf)
	if err != nil {
		d.pch <- &result{e: err}
		return C.int(-1)
	}
	C.memcpy(unsafe.Pointer(cbuf), unsafe.Pointer(&buf[0]), C.ulong(n))
	return C.int(n)
}

//export goWritePacketFunc
func goWritePacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	d := pointer.Restore(opaque).(*DemuxContext)
	b := C.GoBytes(unsafe.Pointer(buf), bufsize)
	p := &rtp.Packet{}
	if err := p.Unmarshal(b); err != nil {
		d.pch <- &result{e: err}
		return C.int(-1)
	}
	if p.PayloadType != 72 {
		// filter out rtcp packets, they're not useful for us
		d.pch <- &result{p: p}
	}
	return bufsize
}

func (c *DemuxContext) init() error {
	outputformat := C.av_guess_format(crtp, nil, nil)
	if outputformat == nil {
		return errors.New("failed to find rtp output format")
	}

	inavformatctx := C.avformat_alloc_context()

	inbuf := C.av_malloc(4096)
	if inbuf == nil {
		return errors.New("failed to allocate buffer")
	}

	inavioctx := C.avio_alloc_context((*C.uchar)(inbuf), 4096, 0, pointer.Save(c), (*[0]byte)(C.cgoReadPacketFunc), nil, nil)
	if inavioctx == nil {
		return errors.New("failed to allocate avio context")
	}

	inavformatctx.pb = inavioctx

	if averr := C.avformat_open_input(&inavformatctx, nil, nil, nil); averr < 0 {
		return av_err("avformat_open_input", averr)
	}

	if averr := C.avformat_find_stream_info(inavformatctx, nil); averr < 0 {
		return av_err("avformat_find_stream_info", averr)
	}

	for i := 0; i < int(inavformatctx.nb_streams); i++ {
		var outavformatctx *C.AVFormatContext
		if averr := C.avformat_alloc_output_context2(&outavformatctx, outputformat, nil, nil); averr < 0 {
			return av_err("avformat_alloc_output_context2", averr)
		}

		outbuf := C.av_malloc(1200)
		if outbuf == nil {
			return errors.New("failed to allocate buffer")
		}

		outavioctx := C.avio_alloc_context((*C.uchar)(outbuf), 1200, 1, pointer.Save(c), nil, (*[0]byte)(C.cgoWritePacketFunc), nil)
		if outavioctx == nil {
			return errors.New("failed to create avio context")
		}

		outavioctx.max_packet_size = 1200

		outavformatctx.pb = outavioctx

		var outstream *C.AVStream
		instream := ((*[1 << 30]*C.AVStream)(unsafe.Pointer(inavformatctx.streams)))[i]
		incodecpar := instream.codecpar

		if incodecpar.codec_type != C.AVMEDIA_TYPE_AUDIO && incodecpar.codec_type != C.AVMEDIA_TYPE_VIDEO {
			continue
		}

		c.streamMapping[i] = len(c.streamMapping)

		outstream = C.avformat_new_stream(outavformatctx, nil)
		if outstream == nil {
			return errors.New("failed to create output stream")
		}

		if averr := C.avcodec_parameters_copy(outstream.codecpar, incodecpar); averr < 0 {
			return av_err("avcodec_parameters_copy", averr)
		}

		// write the header here to pass the payload type options
		var opts *C.AVDictionary
		if averr := C.av_dict_set_int(&opts, C.CString("payload_type"), C.int64_t(96+i), 0); averr < 0 {
			return av_err("av_dict_set_int", averr)
		}
		if averr := C.avformat_write_header(outavformatctx, &opts); averr < 0 {
			return av_err("avformat_write_header", averr)
		}

		c.outAVFormatCtxs = append(c.outAVFormatCtxs, outavformatctx)

		// create a bitstream filter context
		var bsf *C.AVBSFContext
		switch instream.codec.codec_id {
		case C.AV_CODEC_ID_HEVC:
			if averr := C.av_bsf_list_parse_str(ch264_mp4toannexb, &bsf); averr < 0 {
				return av_err("av_bsf_list_parse_str", averr)
			}
		case C.AV_CODEC_ID_H264:
			if averr := C.av_bsf_list_parse_str(ch264_mp4toannexb, &bsf); averr < 0 {
				return av_err("av_bsf_list_parse_str", averr)
			}
		default:
			if averr := C.av_bsf_get_null_filter(&bsf); averr < 0 {
				return av_err("av_bsf_get_null_filter", averr)
			}
		}
		bsf.par_in = incodecpar
		bsf.par_out = outstream.codecpar
		if averr := C.av_bsf_init(bsf); averr < 0 {
			return av_err("av_bsf_init", averr)
		}

		c.outAVBSFCtxs = append(c.outAVBSFCtxs, bsf)
	}

	c.inAVFormatCtx = inavformatctx

	return nil
}

func (c *DemuxContext) ReadRTP() (*rtp.Packet, error) {
	p, ok := <-c.pch
	if !ok {
		return nil, io.EOF
	}
	return p.p, p.e
}

func (c *DemuxContext) RTPCodecParameters() ([]*webrtc.RTPCodecParameters, error) {
	buf := C.av_mallocz(16384)
	if averr := C.av_sdp_create((**C.AVFormatContext)(unsafe.Pointer(&c.outAVFormatCtxs[0])), C.int(len(c.outAVFormatCtxs)), (*C.char)(buf), 16384); averr < 0 {
		return nil, av_err("av_sdp_create", averr)
	}

	s := sdp.SessionDescription{}
	if err := s.Unmarshal([]byte(C.GoString((*C.char)(buf)))); err != nil {
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
				MimeType: mime,
				ClockRate: uint32(clockRate),
				Channels: uint16(channels),
				SDPFmtpLine: fmtp,
			},
		}
	}
	return params, nil
}
