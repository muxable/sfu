package av

/*
#cgo pkg-config: libavcodec libavformat
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
*/
import "C"
import (
	"errors"

	"github.com/pion/webrtc/v3"
)

type AVFormatContext interface {
	AVFormatContext() *C.AVFormatContext
}

type DecodeContext struct {
	Sink       AVFrameWriteCloser
	decoderctx *C.AVCodecContext
	webrtc.RTPCodecType
	packetCh chan *AVPacket
	err      error
	doneCh   chan bool
}

func NewDecoder(demuxer AVFormatContext, stream *AVStream) (*DecodeContext, error) {
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

	if stream.stream.codecpar.codec_type == C.AVMEDIA_TYPE_VIDEO {
		decoderctx.framerate = C.av_guess_frame_rate(demuxer.AVFormatContext(), stream.stream, nil)
	}

	decoderctx.flags |= C.AV_CODEC_FLAG_LOW_DELAY
	decoderctx.flags2 |= C.AV_CODEC_FLAG2_FAST   // accept artifacts at slice edges, https://stackoverflow.com/a/54873148/86433
	decoderctx.flags2 |= C.AV_CODEC_FLAG2_CHUNKS // indicate that the we might truncate at packet boundaries

	if averr := C.avcodec_open2(decoderctx, codec, nil); averr < 0 {
		return nil, av_err("avcodec_open2", averr)
	}

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
		}
	}
}

func (c *DecodeContext) WriteAVPacket(p *AVPacket) error {
	c.packetCh <- p
	return c.err
}

func (c *DecodeContext) Close() error {
	c.packetCh <- &AVPacket{}
	<-c.doneCh
	return c.err
}
