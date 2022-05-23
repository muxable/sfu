package av

/*
#cgo pkg-config: libavcodec libavformat libavutil
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/hwcontext.h>
*/
import "C"
import (
	"errors"

	"github.com/pion/webrtc/v3"
)

type DecodeContext struct {
	Sink       AVFrameWriteCloser
	decoderctx *C.AVCodecContext
	webrtc.RTPCodecType
	packetCh chan *AVPacket
	err      error
	doneCh   chan bool
}

var (
	cthreads = C.CString("threads")
	cauto = C.CString("auto")
)

func NewDecoder(demuxer *DemuxContext, stream *AVStream) (*DecodeContext, error) {
	decoderctx := C.avcodec_alloc_context3(nil)
	if decoderctx == nil {
		return nil, errors.New("failed to create decoder context")
	}

	if averr := C.avcodec_parameters_to_context(decoderctx, stream.stream.codecpar); averr < 0 {
		return nil, av_err("avcodec_parameters_to_context", averr)
	}

	codec := C.avcodec_find_decoder(decoderctx.codec_id)
	if codec == nil {
		return nil, errors.New("failed to find decoder")
	}

	decoderctx.codec_id = codec.id

	if stream.stream.codecpar.codec_type == C.AVMEDIA_TYPE_VIDEO {
		decoderctx.framerate = C.av_guess_frame_rate(demuxer.avformatctx, stream.stream, nil)
	}

	decoderctx.flags2 |= C.AV_CODEC_FLAG2_FAST

	var opts *C.AVDictionary
	defer C.av_dict_free(&opts)
	if averr := C.av_dict_set(&opts, cthreads, cauto, 0); averr < 0 {
		return nil, av_err("av_dict_set", averr)
	}

	if averr := C.avcodec_open2(decoderctx, codec, &opts); averr < 0 {
		return nil, av_err("avcodec_open2", averr)
	}

	stream.stream.discard = C.AVDISCARD_DEFAULT

	codecType := webrtc.RTPCodecType(0)
	switch stream.stream.codecpar.codec_type {
	case C.AVMEDIA_TYPE_AUDIO:
		codecType = webrtc.RTPCodecType(webrtc.RTPCodecTypeAudio)
	case C.AVMEDIA_TYPE_VIDEO:
		codecType = webrtc.RTPCodecType(webrtc.RTPCodecTypeVideo)
	}

	c := &DecodeContext{
		decoderctx:   decoderctx,
		RTPCodecType: codecType,
		packetCh:     make(chan *AVPacket, 10),
		doneCh:       make(chan bool),
	}
	go c.drainLoop()
	return c, nil
}

func (c *DecodeContext) drainLoop() {
	defer func() {
		// close the sink
		if sink := c.Sink; sink != nil {
			if err := sink.Close(); err != nil {
				c.err = err
			}
		}

		// free the context
		C.avcodec_free_context(&c.decoderctx)

		c.doneCh <- true
	}()
	for p := range c.packetCh {
		C.av_packet_rescale_ts(p.packet, p.timebase, c.decoderctx.time_base)
		p.timebase = c.decoderctx.time_base

		if averr := C.avcodec_send_packet(c.decoderctx, p.packet); averr < 0 {
			c.err = av_err("avcodec_send_packet", averr)
			return
		}
		
		p.Unref()

		for {
			f := NewAVFrame()
			if res := C.avcodec_receive_frame(c.decoderctx, f.frame); res < 0 {
				if res == AVERROR(C.EAGAIN) {
					break
				}
				c.err = av_err("failed to receive frame", res)
				return
			}

			if f.frame.pts == C.AV_NOPTS_VALUE {
				continue
			}

			f.frame.pts = f.frame.best_effort_timestamp

			if sink := c.Sink; sink != nil {
				if err := sink.WriteAVFrame(f); err != nil {
					c.err = err
					return
				}
			}
			f.Unref()
		}
	}
}

func (c *DecodeContext) WriteAVPacket(p *AVPacket) error {
	p.Ref()
	c.packetCh <- p
	return c.err
}

func (c *DecodeContext) Close() error {
	c.packetCh <- &AVPacket{}
	<-c.doneCh
	return c.err
}
