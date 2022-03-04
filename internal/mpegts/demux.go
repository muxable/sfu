package mpegts

/*
#cgo pkg-config: libavformat
#include <libavformat/avformat.h>
#include "demux.h"
*/
import "C"
import (
	"errors"
	"io"
	"log"
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/rtp"
	"go.uber.org/zap"
)

var (
	crtp = C.CString("rtp")
)

func init() {
	C.av_log_set_level(56)
}

type DemuxContext struct {
	r io.Reader
	inAVFormatCtx *C.AVFormatContext
	outAVFormatCtxs []*C.AVFormatContext
	pch chan *rtp.Packet
}

func NewDemuxer(r io.Reader) (*DemuxContext, error) {
	c := &DemuxContext{r: r, pch: make(chan *rtp.Packet)}
	if err := c.init(); err != nil {
		return nil, err
	}
	go func() {
		pkt := C.av_packet_alloc()
		defer C.av_packet_free(&pkt)
		defer C.avformat_free_context(c.inAVFormatCtx)
		defer func() {
			for _, ctx := range c.outAVFormatCtxs {
				C.avformat_free_context(ctx)
			}
		}()

		for _, ctx := range c.outAVFormatCtxs {
			if averr := C.avformat_write_header(ctx, nil); averr < 0 {
				panic(averr)
			}
		}

		for {
			averr := C.av_read_frame(c.inAVFormatCtx, pkt)
			if averr < 0 {
				err := av_err("av_read_frame", averr)
				if err == io.EOF {
					return
				}
				panic(err)
			}

			pkt.pos = C.int64_t(-1)

			instream := (*[1<<30]C.AVStream)(unsafe.Pointer(c.inAVFormatCtx.streams))[pkt.stream_index]
			outstream := (*[1<<30]C.AVStream)(unsafe.Pointer(c.outAVFormatCtxs[pkt.stream_index].streams))[0]

			C.av_packet_rescale_ts(pkt, instream.time_base, outstream.time_base)
	
			if averr := C.av_write_frame(c.outAVFormatCtxs[pkt.stream_index], pkt); averr < 0 {
				panic(av_err("av_write_frame", averr))
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
		if err != io.EOF {
			zap.L().Error("failed to read RTP packet", zap.Error(err))
		}
		return C.int(0)
	}
	C.memcpy(unsafe.Pointer(cbuf), unsafe.Pointer(&buf[0]), C.ulong(n))
	return C.int(n)
}

//export goWritePacketFunc
func goWritePacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	m := pointer.Restore(opaque).(*DemuxContext)
	b := C.GoBytes(unsafe.Pointer(buf), bufsize)
	p := &rtp.Packet{}
	if err := p.Unmarshal(b); err != nil {
		zap.L().Error("failed to unmarshal rtp packet", zap.Error(err))
		return C.int(-1)
	}
	m.pch <- p
	return bufsize
}

func (c *DemuxContext) init() error {
	// construct the input context
	inAVFormatCtx := C.avformat_alloc_context()
	if inAVFormatCtx == nil {
		return errors.New("failed to create format context")
	}

	inbuf := C.av_malloc(4096)
	if inbuf == nil {
		return errors.New("failed to allocate buffer")
	}

	inavioctx := C.avio_alloc_context((*C.uchar)(inbuf), 4096, 0, pointer.Save(c), (*[0]byte)(C.cgoReadPacketFunc), nil, nil)
	if inavioctx == nil {
		return errors.New("failed to allocate avio context")
	}

	inAVFormatCtx.pb = inavioctx
	if averr := C.avformat_open_input(&inAVFormatCtx, nil, nil, nil); averr < C.int(0) {
		return av_err("avformat_open_input", averr)
	}

	if averr := C.avformat_find_stream_info(inAVFormatCtx, nil); averr < C.int(0) {
		return av_err("avformat_find_stream_info", averr)
	}

	// create output streams
	instreams := (*[1<<30]C.AVStream)(unsafe.Pointer(inAVFormatCtx.streams))
	for i := 0; i < int(inAVFormatCtx.nb_streams); i++ {
		// construct the output context
		// the rtp muxer only accepts one stream per muxer, so we must create one context
		// per stream despite merging them in memory
		outputformat := C.av_guess_format(crtp, nil, nil)
		if outputformat == nil {
			return errors.New("failed to find rtp output format")
		}

		var outAVFormatCtx *C.AVFormatContext
		if averr := C.avformat_alloc_output_context2(&outAVFormatCtx, outputformat, nil, nil); averr < 0 {
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

		outAVFormatCtx.pb = outavioctx

     	C.av_dump_format(inAVFormatCtx, C.int(i), crtp, 0)

		instream := instreams[i]
		outstream := C.avformat_new_stream(outAVFormatCtx, nil)
		if outstream == nil {
			return errors.New("failed to create output stream")
		}

		log.Printf("copying %d %#v", i, instream)
		if averr := C.avcodec_parameters_copy(outstream.codecpar, instream.codecpar); averr < C.int(0) {
			return av_err("avcodec_parameters_copy", averr)
		}
		c.outAVFormatCtxs = append(c.outAVFormatCtxs, outAVFormatCtx)
	}

	c.inAVFormatCtx = inAVFormatCtx

	return nil
}

func (c *DemuxContext) ReadRTP() (*rtp.Packet, error) {
	p, ok := <-c.pch
	if !ok {
		return nil, io.EOF
	}
	return p, nil
}
