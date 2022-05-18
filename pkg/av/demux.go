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
	"os"
	"unsafe"

	"github.com/mattn/go-pointer"
	"github.com/pion/rtpio/pkg/rtpio"
	"github.com/pion/webrtc/v3"
)

type DemuxContext struct {
	Sinks       []*IndexedSink
	avformatctx *C.AVFormatContext
	rtpin       rtpio.RTPReader
	rawin       io.Reader
}

var (
	csdp              = C.CString("sdp")
	csdpflags         = C.CString("sdp_flags")
	ccustomio         = C.CString("custom_io")
	creorderqueuesize = C.CString("reorder_queue_size")
)

func NewRTPDemuxer(codec webrtc.RTPCodecParameters, in rtpio.RTPReader) (*DemuxContext, error) {
	sdpformat := C.av_find_input_format(csdp)
	if sdpformat == nil {
		return nil, errors.New("could not find sdp format")
	}

	avformatctx := C.avformat_alloc_context()
	if avformatctx == nil {
		return nil, errors.New("failed to create format context")
	}

	// initialize an RTP demuxer
	var opts *C.AVDictionary
	defer C.av_dict_free(&opts)
	if averr := C.av_dict_set(&opts, csdpflags, ccustomio, 0); averr < 0 {
		return nil, av_err("av_dict_set", averr)
	}
	if averr := C.av_dict_set_int(&opts, creorderqueuesize, C.int64_t(768), 0); averr < 0 {
		return nil, av_err("av_dict_set", averr)
	}

	sdpfile, err := NewTempSDP(codec)
	if err != nil {
		return nil, err
	}

	cfilename := C.CString(sdpfile.Name())
	defer C.free(unsafe.Pointer(cfilename))

	if averr := C.avformat_open_input(&avformatctx, cfilename, sdpformat, &opts); averr < 0 {
		return nil, av_err("avformat_open_input", averr)
	}

	buf := C.av_malloc(1500)
	if buf == nil {
		return nil, errors.New("failed to allocate buffer")
	}

	c := &DemuxContext{
		avformatctx: avformatctx,
		rtpin:       in,
	}

	avioctx := C.avio_alloc_context((*C.uchar)(buf), 1500, 1, pointer.Save(c), (*[0]byte)(C.cgoReadBufferFunc), (*[0]byte)(C.cgoWriteRTCPPacketFunc), nil)
	if avioctx == nil {
		return nil, errors.New("failed to allocate avio context")
	}

	avformatctx.pb = avioctx

	if averr := C.avformat_find_stream_info(avformatctx, nil); averr < 0 {
		return nil, av_err("avformat_find_stream_info", averr)
	}

	if err := sdpfile.Close(); err != nil {
		return nil, err
	}

	if err := os.Remove(sdpfile.Name()); err != nil {
		return nil, err
	}

	return c, nil
}

func NewRawDemuxer(in io.Reader) (*DemuxContext, error) {
	avformatctx := C.avformat_alloc_context()
	if avformatctx == nil {
		return nil, errors.New("failed to create format context")
	}

	// initialize a raw demuxer
	buf := C.av_malloc(4096)
	if buf == nil {
		return nil, errors.New("failed to allocate buffer")
	}

	c := &DemuxContext{
		avformatctx: avformatctx,
		rawin:       in,
	}

	avioctx := C.avio_alloc_context((*C.uchar)(buf), 4096, 0, pointer.Save(c), (*[0]byte)(C.cgoReadBufferFunc), nil, nil)
	if avioctx == nil {
		return nil, errors.New("failed to allocate avio context")
	}

	avformatctx.pb = avioctx
	avformatctx.probesize = 65536

	if averr := C.avformat_open_input(&avformatctx, nil, nil, nil); averr < 0 {
		return nil, av_err("avformat_open_input", averr)
	}

	if averr := C.avformat_find_stream_info(avformatctx, nil); averr < 0 {
		return nil, av_err("avformat_find_stream_info", averr)
	}

	return c, nil
}

func (c *DemuxContext) AVFormatContext() *C.AVFormatContext {
	return c.avformatctx
}

//export goReadBufferFunc
func goReadBufferFunc(opaque unsafe.Pointer, cbuf *C.uint8_t, bufsize C.int) C.int {
	d := pointer.Restore(opaque).(*DemuxContext)
	if d.rtpin != nil {
		p, err := d.rtpin.ReadRTP()
		if err != nil {
			if err != io.EOF {
				return AVERROR(C.EIO)
			}
			return AVERROR_EOF
		}

		b, err := p.Marshal()
		if err != nil {
			return AVERROR(C.EINVAL)
		}

		if C.int(len(b)) > bufsize {
			return AVERROR(C.ENOMEM)
		}

		C.memcpy(unsafe.Pointer(cbuf), unsafe.Pointer(&b[0]), C.ulong(len(b)))

		return C.int(len(b))
	}
	buf := make([]byte, int(bufsize))
	n, err := d.rawin.Read(buf)
	if err != nil {
		if err != io.EOF {
			return AVERROR(C.EIO)
		}
		return AVERROR_EOF
	}
	C.memcpy(unsafe.Pointer(cbuf), unsafe.Pointer(&buf[0]), C.ulong(n))
	return C.int(n)
}

//export goWriteRTCPPacketFunc
func goWriteRTCPPacketFunc(opaque unsafe.Pointer, buf *C.uint8_t, bufsize C.int) C.int {
	// this function is necessary: https://trac.ffmpeg.org/ticket/9670
	return bufsize
}

func (c *DemuxContext) Streams() []*AVStream {
	streams := make([]*AVStream, c.avformatctx.nb_streams)
	for i, stream := range (*[1 << 30]*C.AVStream)(unsafe.Pointer(c.avformatctx.streams))[:c.avformatctx.nb_streams] {
		streams[i] = &AVStream{stream}
	}
	return streams
}

func (c *DemuxContext) Run() error {
	streams := c.Streams()
	if len(c.Sinks) != len(streams) {
		return errors.New("number of streams does not match number of sinks")
	}
	for {
		p := NewAVPacket()
		if averr := C.av_read_frame(c.avformatctx, p.packet); averr < 0 {
			return av_err("av_read_frame", averr)
		}
		streamidx := p.packet.stream_index
		if sink := c.Sinks[streamidx]; sink != nil {
			p.timebase = streams[streamidx].stream.time_base
			p.packet.stream_index = C.int(sink.Index)
			if p.packet.stream_index == 1 {
				continue
			}
			if err := sink.WriteAVPacket(p); err != nil {
				return err
			}
		}
	}
}

func (c *DemuxContext) Close() error {
	// close all the sinks
	for _, sink := range c.Sinks {
		if err := sink.Close(); err != nil {
			return err
		}
	}

	// free the context
	C.avformat_free_context(c.avformatctx)

	return nil
}

var _ AVFormatContext = (*DeviceContext)(nil)
