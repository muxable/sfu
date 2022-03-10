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
	"sync"
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
	format      string
	pch         chan *buffer
}

func NewRawMuxer(format string, encoder *EncodeContext) *RawMuxContext {
	c := &RawMuxContext{
		packet:  NewAVPacket(),
		encoder: encoder,
		format:  format,
		pch:     make(chan *buffer, 8), // we need a small buffer here to avoid blocking initialization.
	}
	return c
}

//export goWritePacketFunc
func goWritePacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	m := pointer.Restore(opaque).(*RawMuxContext)
	m.pch <- &buffer{b: C.GoBytes(unsafe.Pointer(buf), bufsize)}
	return bufsize
}

func (c *RawMuxContext) Initialize() error {
	if err := c.encoder.init(); err != nil {
		return err
	}

	cformat := C.CString(c.format)
	defer C.free(unsafe.Pointer(cformat))

	outputformat := C.av_guess_format(cformat, nil, nil)
	if outputformat == nil {
		return errors.New("failed to find rtp output format")
	}

	buf := C.av_malloc(1200)
	if buf == nil {
		return errors.New("failed to allocate buffer")
	}

	var wg sync.WaitGroup
	var avformatctx *C.AVFormatContext

	if averr := C.avformat_alloc_output_context2(&avformatctx, outputformat, nil, nil); averr < 0 {
		return av_err("avformat_alloc_output_context2", averr)
	}

	avioctx := C.avio_alloc_context((*C.uchar)(buf), 1500, 1, pointer.Save(c), nil, (*[0]byte)(C.cgoWritePacketFunc), nil)
	if avioctx == nil {
		return errors.New("failed to create avio context")
	}

	avioctx.max_packet_size = 1500

	avformatctx.pb = avioctx

	for i := 0; i < c.encoder.decoder.Len(); i++ {
		avformatstream := C.avformat_new_stream(avformatctx, nil)
		if avformatstream == nil {
			return errors.New("failed to create rtp stream")
		}

		if averr := C.avcodec_parameters_from_context(avformatstream.codecpar, c.encoder.encoderctxs[i]); averr < 0 {
			return av_err("avcodec_parameters_from_context", averr)
		}
	}
	// write thread
	if averr := C.avformat_write_header(avformatctx, nil); averr < 0 {
		return av_err("avformat_write_header", averr)
	}
	go func() {
		for i := 0; i < c.encoder.decoder.Len(); i++ {
			wg.Add(1)
			go func(i int) {
				for {
					if err := c.encoder.ReadAVPacket(i, c.packet); err != nil {
						if err != io.EOF {
							c.pch <- &buffer{e: err}
						}
						break
					}
					// stream index is always zero because there's n contexts, one stream each.
					c.packet.packet.stream_index = 0
					if averr := C.av_interleaved_write_frame(c.avformatctx, c.packet.packet); averr < 0 {
						c.pch <- &buffer{e: av_err("av_interleaved_write_frame", averr)}
						break
					}
				}
				wg.Done()
			}(i)
		}
		wg.Wait()
		if averr := C.av_write_trailer(c.avformatctx); averr < 0 {
			c.pch <- &buffer{e: av_err("av_write_trailer", averr)}
		}
		close(c.pch)
	}()

	return nil
}

func (c *RawMuxContext) Read(buf []byte) (int, error) {
	r, ok := <-c.pch
	if !ok {
		return 0, io.EOF
	}
	if r.e != nil {
		return 0, r.e
	}
	if len(r.b) < len(buf) {
		return 0, io.ErrShortBuffer
	}
	return copy(buf, r.b), nil
}
