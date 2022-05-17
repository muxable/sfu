package av

/*
#cgo pkg-config: libavcodec libswresample libavutil
#include <libavcodec/avcodec.h>
#include <libswresample/swresample.h>
#include <libavutil/audio_fifo.h>
*/
import "C"
import (
	"errors"
	"io"
	"unsafe"
)

type ResampleContext struct {
	swrctx     *C.SwrContext
	encoderctx *C.AVCodecContext
	decoderctx *C.AVCodecContext
	fifo       *C.AVAudioFifo
	buffer     *C.uint8_t
	frame      *AVFrame
	Sink       AVFrameWriteCloser
	pts        uint64
}

func NewResampler(decoder *DecodeContext, encoder *EncodeContext) (*ResampleContext, error) {
	e := encoder.encoderctx
	d := decoder.decoderctx

	swrctx := C.swr_alloc_set_opts(
		nil,
		C.int64_t(e.channel_layout), /* out_ch_layout */
		e.sample_fmt,                /* out_sample_fmt */
		e.sample_rate,               /* out_sample_rate */
		C.int64_t(d.channel_layout), /* in_ch_layout */
		d.sample_fmt,                /* in_sample_fmt */
		d.sample_rate,               /* in_sample_rate */
		0,                           /* log_offset */
		nil,                         /* log_ctx */
	)
	if swrctx == nil {
		return nil, errors.New("failed to create resampler context")
	}

	if averr := C.swr_init(swrctx); averr < 0 {
		return nil, av_err("swr_init", averr)
	}

	frame := NewAVFrame()
	frame.frame.format = C.int(e.sample_fmt)
	frame.frame.sample_rate = C.int(e.sample_rate)
	frame.frame.channel_layout = C.uint64_t(e.channel_layout)
	frame.frame.nb_samples = C.int(e.frame_size)
	if averr := C.av_frame_get_buffer(frame.frame, 0); averr < 0 {
		return nil, errors.New("failed to allocate resampler frame")
	}

	// create the fifo
	fifo := C.av_audio_fifo_alloc(e.sample_fmt, e.channels, e.frame_size)
	if fifo == nil {
		return nil, errors.New("failed to allocate resampler fifo")
	}

	// create the sample buffer, allocating enough samples for two frames
	var buffer *C.uint8_t
	if averr := C.av_samples_alloc(&buffer, nil, e.channels, 2*e.frame_size, e.sample_fmt, 0); averr < 0 {
		return nil, av_err("av_samples_alloc", averr)
	}

	return &ResampleContext{
		swrctx:     swrctx,
		frame:      frame,
		fifo:       fifo,
		buffer:     buffer,
		encoderctx: e,
		decoderctx: d,
	}, nil
}

func (c *ResampleContext) WriteAVFrame(f *AVFrame) error {
	ifs := C.int(f.frame.nb_samples)
	ofs := C.int(c.frame.frame.nb_samples)

	// c.frame.frame.pkt_dts = f.frame.pkt_dts
	// c.frame.frame.pkt_pos = f.frame.pkt_pos
	// c.frame.frame.pkt_duration = f.frame.pkt_duration

	// convert the samples to the output format
	os := C.swr_convert(c.swrctx, &c.buffer, 2*ofs, f.frame.extended_data, ifs)
	if os < 0 {
		return av_err("swr_convert", os)
	}

	// ensure the samples can be stored in the fifo, realloc if needed
	if averr := C.av_audio_fifo_realloc(c.fifo, C.av_audio_fifo_size(c.fifo)+os); averr < 0 {
		return av_err("av_audio_fifo_realloc", averr)
	}

	// add the samples to the fifo
	if C.av_audio_fifo_write(c.fifo, (*unsafe.Pointer)(unsafe.Pointer(&c.buffer)), os) < os {
		return io.ErrShortWrite
	}

	for C.av_audio_fifo_size(c.fifo) >= ofs {
		// pop off the samples from the fifo in frame size chunks
		if C.av_audio_fifo_read(c.fifo, (*unsafe.Pointer)(unsafe.Pointer(&c.frame.frame.data[0])), ofs) < ofs {
			return io.ErrShortWrite
		}

		c.frame.frame.pts = C.int64_t(c.pts)
		c.pts += uint64(ofs)

		// write the frame to the sink
		if sink := c.Sink; sink != nil {
			if err := sink.WriteAVFrame(c.frame); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *ResampleContext) Close() error {
	// close the sink
	if sink := c.Sink; sink != nil {
		if err := sink.Close(); err != nil {
			return err
		}
	}

	// free the temp buffer
	C.av_freep(unsafe.Pointer(&c.buffer))

	// free the fifo
	C.av_audio_fifo_free(c.fifo)

	// free the context
	C.swr_free(&c.swrctx)

	return nil
}
