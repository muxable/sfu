package av

/*
#cgo pkg-config: libavcodec libavformat libavutil
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/log.h>
*/
import "C"
import "runtime"

func init() {
	C.av_log_set_level(56)
}

// These are useful to avoid leaking the cgo interface.

type AVPacket struct {
	packet *C.AVPacket
}

func NewAVPacket() *AVPacket {
	packet := C.av_packet_alloc()
	if packet == nil {
		return nil
	}
	return &AVPacket{packet: packet}
}

func (p *AVPacket) Close() error {
	C.av_packet_free(&p.packet)
	return nil
}

type AVFrame struct {
	frame *C.AVFrame
}

func NewAVFrame() *AVFrame {
	frame := C.av_frame_alloc()
	if frame == nil {
		return nil
	}
	return &AVFrame{frame: frame}
}

func (f *AVFrame) Close() error {
	C.av_frame_free(&f.frame)
	return nil
}

type AVMediaType int

const (
	AVMediaTypeUnknown    AVMediaType = C.AVMEDIA_TYPE_UNKNOWN
	AVMediaTypeVideo      AVMediaType = C.AVMEDIA_TYPE_VIDEO
	AVMediaTypeAudio      AVMediaType = C.AVMEDIA_TYPE_AUDIO
	AVMediaTypeData       AVMediaType = C.AVMEDIA_TYPE_DATA
	AVMediaTypeSubtitle   AVMediaType = C.AVMEDIA_TYPE_SUBTITLE
	AVMediaTypeAttachment AVMediaType = C.AVMEDIA_TYPE_ATTACHMENT
	AVMediaTypeNB         AVMediaType = C.AVMEDIA_TYPE_NB
)

type AVCodecParameters struct {
	codecpar *C.AVCodecParameters
}

func NewAVCodecParametersFromEncoder(ctx *EncodeContext) *AVCodecParameters {
	codecpar := C.avcodec_parameters_alloc()
	if codecpar == nil {
		return nil
	}
	if res := C.avcodec_parameters_from_context(codecpar, ctx.encoderctx); res < 0 {
		C.avcodec_parameters_free(&codecpar)
		return nil
	}
	p := &AVCodecParameters{codecpar: codecpar}
	runtime.SetFinalizer(p, func(p *AVCodecParameters) {
		C.avcodec_parameters_free(&p.codecpar)
	})
	return p
}

func NewAVCodecParametersFromStream(ctx *AVStream) *AVCodecParameters {
	codecpar := C.avcodec_parameters_alloc()
	if codecpar == nil {
		return nil
	}
	if res := C.avcodec_parameters_copy(codecpar, ctx.stream.codecpar); res < 0 {
		C.avcodec_parameters_free(&codecpar)
		return nil
	}
	p := &AVCodecParameters{codecpar: codecpar}
	runtime.SetFinalizer(p, func(p *AVCodecParameters) {
		C.avcodec_parameters_free(&p.codecpar)
	})
	return p
}

// AVStream is a wrapper around libavformat's AVStream.
// Since it is typically owned by a parent, memory management
// is not provided.
type AVStream struct {
	stream *C.AVStream
}

func (s *AVStream) AVMediaType() AVMediaType {
	return AVMediaType(s.stream.codecpar.codec_type)
}

type IndexedSink struct {
	AVPacketWriteCloser
	Index  int
}