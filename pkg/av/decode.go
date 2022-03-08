package av

/*
#cgo pkg-config: libavcodec libavformat
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
*/
import "C"
import (
	"errors"
	"io"
	"unsafe"
)

type DecodeContext struct {
	decoderctxs []*C.AVCodecContext
	pkt         *AVPacket
	demuxer     *DemuxContext
}

func NewDecoder(demuxer *DemuxContext) *DecodeContext {
	return &DecodeContext{
		pkt:     NewAVPacket(),
		demuxer: demuxer,
	}
}

func (c *DecodeContext) init() error {
	if err := c.demuxer.init(); err != nil {
		return err
	}

	for i := 0; i < int(c.demuxer.avformatctx.nb_streams); i++ {
		stream := ((*[1 << 30]*C.AVStream)(unsafe.Pointer(c.demuxer.avformatctx.streams)))[i]

		codec := C.avcodec_find_decoder(stream.codecpar.codec_id)
		if codec == nil {
			return errors.New("failed to find decoder")
		}

		decoderctx := C.avcodec_alloc_context3(stream.codec.codec)
		if decoderctx == nil {
			return errors.New("failed to create decoder context")
		}
		
		// filter out non-audio/video streams
		if codec._type != C.AVMEDIA_TYPE_AUDIO && codec._type != C.AVMEDIA_TYPE_VIDEO {
			continue
		}

		if averr := C.avcodec_parameters_to_context(decoderctx, stream.codecpar); averr < 0 {
			return av_err("avcodec_parameters_to_context", averr)
		}

		if averr := C.avcodec_open2(decoderctx, codec, nil); averr < 0 {
			return av_err("avcodec_open2", averr)
		}

		c.decoderctxs = append(c.decoderctxs, decoderctx)
	}

	return nil
}

func (c *DecodeContext) Len() int {
	return int(c.demuxer.avformatctx.nb_streams)
}

func (c *DecodeContext) ReadAVFrame(index int, f *AVFrame) error {
	if res := C.avcodec_receive_frame(c.decoderctxs[index], f.frame); res < 0 {
		if res == AVERROR(C.EAGAIN) {
			err := c.demuxer.ReadAVPacket(c.pkt)
			if err != nil && err != io.EOF {
				return err
			}

			if averr := C.avcodec_send_packet(c.decoderctxs[index], c.pkt.packet); averr < 0 {
				return av_err("avcodec_send_packet", averr)
			}

			// try again.
			return c.ReadAVFrame(index, f)
		}
		C.avcodec_free_context(&c.decoderctxs[index])
		if err := c.pkt.Close(); err != nil {
			return err
		}
		return av_err("failed to receive frame", res)
	}
	return nil
}
