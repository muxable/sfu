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
	"unsafe"

	"github.com/pion/webrtc/v3"
)

type EncodeContext struct {
	encoderctx      *C.AVCodecContext
	packet          *AVPacket
	Sink            *IndexedSink
	requestKeyframe bool
}

type EncoderConfiguration struct {
	Name    string // if unset, encoder will resolve by output type.
	Bitrate int64
	Codec   webrtc.RTPCodecCapability
	Options map[string]interface{}

	// video parameters
	Width                        uint32
	Height                       uint32
	SampleAspectRatioNumerator   uint32
	SampleAspectRatioDenominator uint32
	FrameRateNumerator           uint32
	FrameRateDenominator         uint32
}

func NewEncoder(config *EncoderConfiguration) (*EncodeContext, error) {
	var encodercodec *C.AVCodec
	if config.Name == "" {
		encodercodec = C.avcodec_find_encoder(AvCodec[config.Codec.MimeType])

		// also autoconfigure options if necessary.
		if config.Options == nil {
			switch config.Codec.MimeType {
			case webrtc.MimeTypeH264:
				config.Options = DefaultH264EncoderOptions
			case webrtc.MimeTypeVP8:
				config.Options = DefaultVP8EncoderOptions
			case webrtc.MimeTypeVP9:
				config.Options = DefaultVP9EncoderOptions
			}
		}
	} else {
		cname := C.CString(config.Name)
		defer C.free(unsafe.Pointer(cname))
		encodercodec = C.avcodec_find_encoder_by_name(cname)
	}
	if encodercodec == nil {
		return nil, errors.New("failed to start encoder")
	}

	encoderctx := C.avcodec_alloc_context3(encodercodec)
	if encoderctx == nil {
		return nil, errors.New("failed to create encoder context")
	}

	var opts *C.AVDictionary
	defer C.av_dict_free(&opts)

	if strings.HasPrefix(config.Codec.MimeType, "audio/") {
		encoderctx.channels = C.int(config.Codec.Channels)
		encoderctx.channel_layout = C.uint64_t(C.av_get_default_channel_layout(C.int(config.Codec.Channels)))
		encoderctx.sample_rate = C.int(config.Codec.ClockRate)
		encoderctx.sample_fmt = C.AV_SAMPLE_FMT_S16
		encoderctx.time_base = C.AVRational{1, C.int(config.Codec.ClockRate)}

		if config.Bitrate > 0 {
			encoderctx.bit_rate = C.int64_t(config.Bitrate)
		} else {
			encoderctx.bit_rate = 96 * 1000
		}
	}
	if strings.HasPrefix(config.Codec.MimeType, "video/") {
		encoderctx.height = C.int(config.Height)
		encoderctx.width = C.int(config.Width)
		encoderctx.sample_aspect_ratio = C.AVRational{C.int(config.SampleAspectRatioNumerator), C.int(config.SampleAspectRatioDenominator)}
		encoderctx.pix_fmt = C.AV_PIX_FMT_YUV420P
		// this is correct, the fraction is inverted.
		// encoderctx.framerate = C.AVRational{C.int(config.FrameRateNumerator), C.int(config.FrameRateDenominator)}
		encoderctx.time_base = C.AVRational{C.int(config.FrameRateDenominator), C.int(config.FrameRateNumerator)}

		encoderctx.max_b_frames = 0
		if config.Bitrate > 0 {
			encoderctx.bit_rate = C.int64_t(config.Bitrate)
		} else {
			encoderctx.bit_rate = 20 * 1000 * 1000
		}
	}

	if config.Options != nil {
		for k, v := range config.Options {
			ckey := C.CString(k)
			defer C.free(unsafe.Pointer(ckey))
			switch v := v.(type) {
			case int:
				if averr := C.av_dict_set_int(&opts, ckey, C.int64_t(v), 0); averr < 0 {
					return nil, av_err("failed to set option", averr)
				}
			case string:
				cval := C.CString(v)
				defer C.free(unsafe.Pointer(cval))
				if averr := C.av_dict_set(&opts, ckey, cval, 0); averr < 0 {
					return nil, av_err("failed to set option", averr)
				}
			}
		}
	}

	if averr := C.avcodec_open2(encoderctx, encodercodec, &opts); averr < 0 {
		return nil, av_err("avcodec_open2", averr)
	}

	return &EncodeContext{
		packet:     NewAVPacket(),
		encoderctx: encoderctx,
	}, nil
}

func (c *EncodeContext) RequestKeyframe() {
	c.requestKeyframe = true
}

func (c *EncodeContext) SetBitrate(bitrate int64) {
	c.encoderctx.bit_rate = C.int64_t(bitrate)
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
			c.packet.timebase = c.encoderctx.time_base
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
