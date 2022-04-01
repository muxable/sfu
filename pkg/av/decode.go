package av

/*
#cgo pkg-config: libavcodec libavformat
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
*/
import "C"
import (
	"errors"
)

type DecodeContext struct {
	Sink       AVFrameWriteCloser
	decoderctx *C.AVCodecContext
	frame      *AVFrame
}

func NewDecoder(demuxer *DemuxContext, stream *AVStream) (*DecodeContext, error) {
	codec := C.avcodec_find_decoder(stream.stream.codecpar.codec_id)
	if codec == nil {
		return nil, errors.New("failed to find decoder")
	}

	decoderctx := C.avcodec_alloc_context3(codec)
	if decoderctx == nil {
		return nil, errors.New("failed to create decoder context")
	}

	if averr := C.avcodec_parameters_to_context(decoderctx, stream.stream.codecpar); averr < 0 {
		return nil, av_err("avcodec_parameters_to_context", averr)
	}

	if stream.stream.codec.codec_type == C.AVMEDIA_TYPE_VIDEO {
		decoderctx.framerate = C.av_guess_frame_rate(demuxer.avformatctx, stream.stream, nil)
	}

	decoderctx.flags |= C.AV_CODEC_FLAG_LOW_DELAY
	decoderctx.flags2 |= C.AV_CODEC_FLAG2_FAST   // accept artifacts at slice edges, https://stackoverflow.com/a/54873148/86433
	decoderctx.flags2 |= C.AV_CODEC_FLAG2_CHUNKS // indicate that the we might truncate at packet boundaries

	if averr := C.avcodec_open2(decoderctx, codec, nil); averr < 0 {
		return nil, av_err("avcodec_open2", averr)
	}

	return &DecodeContext{
		frame:      NewAVFrame(),
		decoderctx: decoderctx,
	}, nil
}

func (c *DecodeContext) WriteAVPacket(p *AVPacket) error {
	C.av_packet_rescale_ts(p.packet, p.timebase, c.decoderctx.time_base)
	p.timebase = c.decoderctx.time_base

	if averr := C.avcodec_send_packet(c.decoderctx, p.packet); averr < 0 {
		return av_err("avcodec_send_packet", averr)
	}

	for {
		if res := C.avcodec_receive_frame(c.decoderctx, c.frame.frame); res < 0 {
			if res == AVERROR(C.EAGAIN) {
				return nil
			}
			return av_err("failed to receive frame", res)
		}

		c.frame.frame.pts = c.frame.frame.best_effort_timestamp

		if sink := c.Sink; sink != nil {
			if err := sink.WriteAVFrame(c.frame); err != nil {
				return err
			}
		}
	}
}

func (c *DecodeContext) Close() error {
	// drain the context
	// if err := c.WriteAVPacket(&AVPacket{}); err != nil && err != io.EOF {
	// 	return err
	// }

	// close the sink
	if sink := c.Sink; sink != nil {
		if err := sink.Close(); err != nil {
			return err
		}
	}

	// close the frame
	if err := c.frame.Close(); err != nil {
		return err
	}

	// free the context
	C.avcodec_free_context(&c.decoderctx)

	return nil
}
