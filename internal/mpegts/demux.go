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
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

type DemuxContext struct {
	r io.Reader
	avformatctx *C.AVFormatContext
}

func NewDemuxer(r io.Reader) ([]*webrtc.TrackLocalStaticRTP, error) {
	c := &DemuxContext{r: r}
	if err := c.init(); err != nil {
		return nil, err
	}

}

//export goReadPacketFunc
func goReadPacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	d := pointer.Restore(opaque).(*DemuxContext)
	p, err := d.in.ReadRTP()
	if err != nil {
		if err != io.EOF {
			zap.L().Error("failed to read RTP packet", zap.Error(err))
		}
		return C.int(0)
	}

	b, err := p.Marshal()
	if err != nil {
		zap.L().Error("failed to marshal RTP packet", zap.Error(err))
		return C.int(0)
	}

	if C.int(len(b)) > bufsize {
		zap.L().Error("RTP packet too large", zap.Int("size", len(b)))
		return C.int(0)
	}

	C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&b[0]), C.ulong(len(b)))

	return C.int(len(b))
}


//export goWriteRTCPPacketFunc
func goWriteRTCPPacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	// this function is necessary: https://trac.ffmpeg.org/ticket/9670
	return bufsize
}

func (c *DemuxContext) init() error {
	inavformatctx := C.avformat_alloc_context()
	if inavformatctx == nil {
		return errors.New("failed to create format context")
	}
	buf := C.av_malloc(1500)
	if buf == nil {
		return errors.New("failed to allocate buffer")
	}

	avioctx := C.avio_alloc_context((*C.uchar)(buf), 1500, 0, pointer.Save(c), (*[0]byte)(C.cgoReadPacketFunc), nil, nil)
	if avioctx == nil {
		return errors.New("failed to allocate avio context")
	}

	inavformatctx.pb = avioctx

	if averr := C.avformat_open_input(&inavformatctx, nil, nil, nil); averr < C.int(0) {
		return av_err("avformat_open_input", averr)
	}

	if averr := C.avformat_find_stream_info(inavformatctx, nil); averr < C.int(0) {
		return av_err("avformat_find_stream_info", averr)
	}

	c.avformatctx = inavformatctx

	outavformatctx := C.avformat_alloc_context()

	for i := 0; i < int(inavformatctx.nb_streams); i++ {
		instream = inavformatctx.streams[i]
		outstream := C.avformat_new_stream(outavformatctx, nil)
		if outstream == nil {
			return errors.New("failed to create output stream")
		}

		if averr := C.avcodec_parameters_copy(outstream.codecpar, instream.codecpar); averr < C.int(0) {
			return av_err("avcodec_parameters_copy", averr)
		}
	}

	return nil
}

func (c *DemuxContext) ReadAVPacket(p *AVPacket) error {
	averr := C.av_read_frame(c.avformatctx, p.packet)
	if averr < 0 {
		err := av_err("av_read_frame", averr)
		if err == io.EOF {
			// TODO: is this necessary? does ffmpeg do it automatically?
			p.packet = nil
		}
		C.avformat_free_context(c.avformatctx)
		return err
	}
	return nil
}
