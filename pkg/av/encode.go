package av

/*
#cgo pkg-config: libavcodec libavformat libavutil
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
#include <libavutil/channel_layout.h>
*/
import "C"
import (
	"errors"
	"strings"

	"github.com/pion/webrtc/v3"
)

type EncodeContext struct {
	codec           webrtc.RTPCodecCapability
	encoderctx      *C.AVCodecContext
	packet          *AVPacket
	Sink            *IndexedSink
	requestKeyframe bool
}

func NewEncoder(codec webrtc.RTPCodecCapability, decoder *DecodeContext) (*EncodeContext, error) {
	encodercodec := C.avcodec_find_encoder(AvCodec[codec.MimeType])
	if encodercodec == nil {
		return nil, errors.New("failed to start encoder")
	}

	encoderctx := C.avcodec_alloc_context3(encodercodec)
	if encoderctx == nil {
		return nil, errors.New("failed to create encoder context")
	}

	if strings.HasPrefix(codec.MimeType, "audio/") {
		encoderctx.channels = C.int(codec.Channels)
		encoderctx.channel_layout = C.uint64_t(C.av_get_default_channel_layout(C.int(codec.Channels)))
		encoderctx.sample_rate = C.int(codec.ClockRate)
		encoderctx.sample_fmt = C.AV_SAMPLE_FMT_S16
	}
	if strings.HasPrefix(codec.MimeType, "video/") {
		encoderctx.width = decoder.decoderctx.width
		encoderctx.height = decoder.decoderctx.height
		encoderctx.pix_fmt = C.AV_PIX_FMT_YUV420P
		encoderctx.time_base = decoder.decoderctx.time_base
	}

	var opts *C.AVDictionary
	defer C.av_dict_free(&opts)

	switch codec.MimeType {
	case webrtc.MimeTypeH264:
		if averr := C.av_dict_set(&opts, C.CString("preset"), C.CString("ultrafast"), 0); averr < 0 {
			return nil, av_err("av_dict_set", averr)
		}
		if averr := C.av_dict_set(&opts, C.CString("tune"), C.CString("zerolatency"), 0); averr < 0 {
			return nil, av_err("av_dict_set", averr)
		}
		if averr := C.av_dict_set(&opts, C.CString("profile"), C.CString("high"), 0); averr < 0 {
			return nil, av_err("av_dict_set", averr)
		}
		if averr := C.av_dict_set(&opts, C.CString("level"), C.CString("5.0"), 0); averr < 0 {
			return nil, av_err("av_dict_set", averr)
		}

		encoderctx.bit_rate = 20 * 1000 * 1000
		encoderctx.rc_buffer_size = 4 * 1000 * 1000
		encoderctx.rc_max_rate = 20 * 1000 * 1000
		encoderctx.rc_min_rate = 1 * 1000 * 1000
	case webrtc.MimeTypeVP8:
		if averr := C.av_dict_set(&opts, C.CString("deadline"), C.CString("realtime"), 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		if averr := C.av_dict_set_int(&opts, C.CString("cpu-used"), 5, 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		// if averr := C.av_dict_set_int(&opts, C.CString("error-resilient"), 1, 0); averr < 0 {
		// 	return nil, av_err("av_dict_set_int", averr)
		// }
		// if averr := C.av_dict_set_int(&opts, C.CString("auto-alt-ref"), 1, 0); averr < 0 {
		// 	return nil, av_err("av_dict_set_int", averr)
		// }
		if averr := C.av_dict_set_int(&opts, C.CString("crf"), 20, 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}

		encoderctx.max_b_frames = 0
		encoderctx.bit_rate = 20 * 1000 * 1000
		encoderctx.gop_size = 10
	case webrtc.MimeTypeVP9:
		if averr := C.av_dict_set(&opts, C.CString("deadline"), C.CString("realtime"), 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		if averr := C.av_dict_set_int(&opts, C.CString("cpu-used"), 5, 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		// if averr := C.av_dict_set_int(&opts, C.CString("error-resilient"), 1, 0); averr < 0 {
		// 	return nil, av_err("av_dict_set_int", averr)
		// }
		// if averr := C.av_dict_set_int(&opts, C.CString("auto-alt-ref"), 1, 0); averr < 0 {
		// 	return nil, av_err("av_dict_set_int", averr)
		// }
		if averr := C.av_dict_set_int(&opts, C.CString("crf"), 20, 0); averr < 0 {
			return nil, av_err("av_dict_set_int", averr)
		}
		
		encoderctx.max_b_frames = 0
		encoderctx.bit_rate = 20 * 1000 * 1000
		encoderctx.gop_size = 10
	case webrtc.MimeTypeOpus:
		encoderctx.bit_rate = 96 * 1000
	}

	if averr := C.avcodec_open2(encoderctx, encodercodec, &opts); averr < 0 {
		return nil, av_err("avcodec_open2", averr)
	}

	return &EncodeContext{
		codec:      codec,
		packet:     NewAVPacket(),
		encoderctx: encoderctx,
	}, nil
}

func (c *EncodeContext) RequestKeyframe() {
	c.requestKeyframe = true
}

func (c *EncodeContext) WriteAVFrame(f *AVFrame) error {
	// erase the picture types so the encoder can set them
	if f.frame != nil {
		if c.requestKeyframe {
			f.frame.pict_type = C.AV_PICTURE_TYPE_I
			c.requestKeyframe = false
		} else {
			f.frame.pict_type = C.AV_PICTURE_TYPE_NONE
		}
	}

	if res := C.avcodec_send_frame(c.encoderctx, f.frame); res < 0 {
		return av_err("avcodec_send_frame", res)
	}

	for {
		C.av_new_packet(c.packet.packet, c.packet.packet.size)
		if res := C.avcodec_receive_packet(c.encoderctx, c.packet.packet); res < 0 {
			if res == AVERROR(C.EAGAIN) {
				return nil
			}
			return av_err("failed to receive frame", res)
		}

		if sink := c.Sink; sink != nil {
			c.packet.packet.stream_index = C.int(sink.Index)
			if err := sink.WriteAVPacket(c.packet); err != nil {
				return err
			}
		}
	}
}

func (c *EncodeContext) Close() error {
	// drain the context
	// if err := c.WriteAVFrame(&AVFrame{}); err != nil && err != io.EOF {
	// 	return err
	// }

	// close the sink
	if sink := c.Sink; sink != nil {
		if err := sink.Close(); err != nil {
			return err
		}
	}

	// close the packet
	if err := c.packet.Close(); err != nil {
		return err
	}

	// free the context
	C.avcodec_free_context(&c.encoderctx)

	return nil
}
