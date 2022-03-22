package av

/*
#cgo pkg-config: libavcodec libavformat
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include "mux.h"
*/
import "C"
import (
	"errors"
	"io"
	"unsafe"

	"github.com/mattn/go-pointer"
)

type buffer struct {
	b []byte
	e error
}
type RawMuxContext struct {
	avformatctx *C.AVFormatContext
	packet      *AVPacket
	encoder     *EncodeContext
	Sink        io.WriteCloser

	headerWritten bool
}

func NewRawMuxer(format string, params []*AVCodecParameters) (*RawMuxContext, error) {
	cformat := C.CString(format)
	defer C.free(unsafe.Pointer(cformat))

	outputformat := C.av_guess_format(cformat, nil, nil)
	if outputformat == nil {
		return nil, errors.New("failed to find rtp output format")
	}

	buf := C.av_malloc(1316)
	if buf == nil {
		return nil, errors.New("failed to allocate buffer")
	}

	var avformatctx *C.AVFormatContext

	if averr := C.avformat_alloc_output_context2(&avformatctx, outputformat, nil, nil); averr < 0 {
		return nil, av_err("avformat_alloc_output_context2", averr)
	}

	c := &RawMuxContext{avformatctx: avformatctx}

	avioctx := C.avio_alloc_context((*C.uchar)(buf), 1316, 1, pointer.Save(c), nil, (*[0]byte)(C.cgoWritePacketFunc), nil)
	if avioctx == nil {
		return nil, errors.New("failed to create avio context")
	}

	avioctx.max_packet_size = 1316

	avformatctx.flags |= C.AVFMT_FLAG_NOBUFFER
	avformatctx.pb = avioctx

	for _, param := range params {
		avformatstream := C.avformat_new_stream(avformatctx, nil)
		if avformatstream == nil {
			return nil, errors.New("failed to create rtp stream")
		}

		if averr := C.avcodec_parameters_copy(avformatstream.codecpar, param.codecpar); averr < 0 {
			return nil, av_err("avcodec_parameters_copy", averr)
		}
	}
	return c, nil
}

//export goWritePacketFunc
func goWritePacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	m := pointer.Restore(opaque).(*RawMuxContext)
	n, err := m.Sink.Write(C.GoBytes(unsafe.Pointer(buf), bufsize))
	if err != nil {
		return -1
	}
	return C.int(n)
}

func (c *RawMuxContext) WriteAVPacket(p *AVPacket) error {
	if !c.headerWritten {
		// write thread
		if averr := C.avformat_write_header(c.avformatctx, nil); averr < 0 {
			return av_err("avformat_write_header", averr)
		}
		c.headerWritten = true
	}
	if averr := C.av_interleaved_write_frame(c.avformatctx, p.packet); averr < 0 {
		return av_err("av_interleaved_write_frame", averr)
	}
	return nil
}

func (c *RawMuxContext) Close() error {
	if averr := C.av_interleaved_write_frame(c.avformatctx, nil); averr < 0 {
		return av_err("av_interleaved_write_frame", averr)
	}

	if averr := C.av_write_trailer(c.avformatctx); averr < 0 {
		return av_err("av_write_trailer", averr)
	}

	// close the sink
	if sink := c.Sink; sink != nil {
		if err := sink.Close(); err != nil {
			return err
		}
	}

	C.avformat_free_context(c.avformatctx)

	return nil
}
