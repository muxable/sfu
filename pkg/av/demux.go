package av

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
	"os"
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
	"go.uber.org/zap"
)

type DemuxContext struct {
	codec       webrtc.RTPCodecParameters
	avformatctx *C.AVFormatContext
	rtpin       rtpio.RTPReader
	rawin       io.Reader
	sdpfile     *os.File
}

var (
	csdpflags         = C.CString("sdp_flags")
	ccustomio         = C.CString("custom_io")
	creorderqueuesize = C.CString("reorder_queue_size")
)

func NewRTPDemuxer(codec webrtc.RTPCodecParameters, in rtpio.RTPReader) *DemuxContext {
	return &DemuxContext{
		codec: codec,
		rtpin: in,
	}
}

func NewRawDemuxer(in io.Reader) *DemuxContext {
	return &DemuxContext{rawin: in}
}

//export goReadBufferFunc
func goReadBufferFunc(opaque unsafe.Pointer, cbuf *C.uint8_t, bufsize C.int) C.int {
	d := pointer.Restore(opaque).(*DemuxContext)
	if d.rtpin != nil {
		p, err := d.rtpin.ReadRTP()
		if err != nil {
			if err == io.EOF {
				d.sdpfile.Close()
				os.Remove(d.sdpfile.Name())
			} else {
				zap.L().Error("failed to read RTP packet", zap.Error(err))
			}
			return C.int(-1)
		}

		b, err := p.Marshal()
		if err != nil {
			zap.L().Error("failed to marshal RTP packet", zap.Error(err))
			return C.int(-1)
		}

		if C.int(len(b)) > bufsize {
			zap.L().Error("RTP packet too large", zap.Int("size", len(b)))
			return C.int(-1)
		}

		C.memcpy(unsafe.Pointer(cbuf), unsafe.Pointer(&b[0]), C.ulong(len(b)))

		return C.int(len(b))
	}
	buf := make([]byte, int(bufsize))
	n, err := d.rawin.Read(buf)
	if err != nil {
		zap.L().Error("failed to read", zap.Error(err))
		return C.int(-1)
	}
	C.memcpy(unsafe.Pointer(cbuf), unsafe.Pointer(&buf[0]), C.ulong(n))
	return C.int(n)
}

//export goWriteRTCPPacketFunc
func goWriteRTCPPacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	// this function is necessary: https://trac.ffmpeg.org/ticket/9670
	return bufsize
}

func (c *DemuxContext) init() error {
	avformatctx := C.avformat_alloc_context()
	if avformatctx == nil {
		return errors.New("failed to create format context")
	}

	if c.rtpin != nil {
		// initialize an RTP demuxer
		var opts *C.AVDictionary
		defer C.av_dict_free(&opts)
		if averr := C.av_dict_set(&opts, csdpflags, ccustomio, 0); averr < 0 {
			return av_err("av_dict_set", averr)
		}
		if averr := C.av_dict_set_int(&opts, creorderqueuesize, C.int64_t(0), 0); averr < 0 {
			return av_err("av_dict_set", averr)
		}

		sdpfile, err := NewTempSDP(c.codec)
		if err != nil {
			return err
		}

		cfilename := C.CString(sdpfile.Name())
		defer C.free(unsafe.Pointer(cfilename))

		buf := C.av_malloc(1500)
		if buf == nil {
			return errors.New("failed to allocate buffer")
		}

		avioctx := C.avio_alloc_context((*C.uchar)(buf), 1500, 1, pointer.Save(c), (*[0]byte)(C.cgoReadBufferFunc), (*[0]byte)(C.cgoWriteRTCPPacketFunc), nil)
		if avioctx == nil {
			return errors.New("failed to allocate avio context")
		}

		avformatctx.pb = avioctx

		if averr := C.avformat_open_input(&avformatctx, cfilename, nil, &opts); averr < 0 {
			return av_err("avformat_open_input", averr)
		}
		
		c.sdpfile = sdpfile
	} else {
		// initialize a raw demuxer
		buf := C.av_malloc(4096)
		if buf == nil {
			return errors.New("failed to allocate buffer")
		}

		avioctx := C.avio_alloc_context((*C.uchar)(buf), 4096, 0, pointer.Save(c), (*[0]byte)(C.cgoReadBufferFunc), nil, nil)
		if avioctx == nil {
			return errors.New("failed to allocate avio context")
		}

		avformatctx.pb = avioctx

		if averr := C.avformat_open_input(&avformatctx, nil, nil, nil); averr < 0 {
			return av_err("avformat_open_input", averr)
		}
	}

	if averr := C.avformat_find_stream_info(avformatctx, nil); averr < 0 {
		return av_err("avformat_find_stream_info", averr)
	}

	c.avformatctx = avformatctx

	return nil
}

func (c *DemuxContext) ReadAVPacket(p *AVPacket) error {
	if averr := C.av_read_frame(c.avformatctx, p.packet); averr < 0 {
		log.Printf("got error")
		err := av_err("av_read_frame", averr)
		if err == io.EOF {
			// TODO: is this necessary? does ffmpeg do it automatically?
			p.packet = nil
		}
		C.avformat_free_context(c.avformatctx)
		if c.sdpfile != nil {
			if err := c.sdpfile.Close(); err != nil {
				return err
			}
		}
		return err
	}
	return nil
}
