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

	"github.com/pion/webrtc/v3"
)

type EncodeContext struct {
	audio       webrtc.RTPCodecCapability
	video       webrtc.RTPCodecCapability
	encoderctxs []*C.AVCodecContext
	frame       *AVFrame
	decoder     *DecodeContext
}

func NewEncoder(audio, video webrtc.RTPCodecCapability, decoder *DecodeContext) *EncodeContext {
	return &EncodeContext{
		audio:   audio,
		video:   video,
		frame:   NewAVFrame(),
		decoder: decoder,
	}
}

func (c *EncodeContext) init() error {
	if err := c.decoder.init(); err != nil {
		return err
	}

	for i, decoderctx := range c.decoder.decoderctxs {
		var codec webrtc.RTPCodecCapability
		if decoderctx.codec_type == C.AVMEDIA_TYPE_AUDIO {
			codec = c.audio
		} else if decoderctx.codec_type == C.AVMEDIA_TYPE_VIDEO {
			codec = c.video
		}

		encodercodec := C.avcodec_find_encoder(AvCodec[codec.MimeType])
		if encodercodec == nil {
			return errors.New("failed to start encoder")
		}

		encoderctx := C.avcodec_alloc_context3(encodercodec)
		if encoderctx == nil {
			return errors.New("failed to create encoder context")
		}

		encoderctx.channels = decoderctx.channels
		encoderctx.channel_layout = decoderctx.channel_layout
		encoderctx.sample_rate = decoderctx.sample_rate
		encoderctx.sample_fmt = decoderctx.sample_fmt
		encoderctx.width = decoderctx.width
		encoderctx.height = decoderctx.height
		encoderctx.pix_fmt = C.AV_PIX_FMT_YUV420P
		instream := ((*[1 << 30]*C.AVStream)(unsafe.Pointer(c.decoder.demuxer.avformatctx.streams)))[i]
		encoderctx.time_base = C.av_guess_frame_rate(c.decoder.demuxer.avformatctx, instream, nil)

		var opts *C.AVDictionary
		defer C.av_dict_free(&opts)

		if codec.MimeType == webrtc.MimeTypeH264 {
			if averr := C.av_dict_set(&opts, C.CString("preset"), C.CString("ultrafast"), 0); averr < 0 {
				return av_err("av_dict_set", averr)
			}
			if averr := C.av_dict_set(&opts, C.CString("tune"), C.CString("zerolatency"), 0); averr < 0 {
				return av_err("av_dict_set", averr)
			}
			if averr := C.av_dict_set(&opts, C.CString("profile"), C.CString("high"), 0); averr < 0 {
				return av_err("av_dict_set", averr)
			}
		}

		if averr := C.avcodec_open2(encoderctx, encodercodec, &opts); averr < 0 {
			return av_err("avcodec_open2", averr)
		}

		encoderctx.rc_buffer_size = 4 * 1000 * 1000
		encoderctx.rc_max_rate = 20 * 1000 * 1000
		encoderctx.rc_min_rate = 1 * 1000 * 1000

		c.encoderctxs = append(c.encoderctxs, encoderctx)
	}

	return nil
}

func (c *EncodeContext) ReadAVPacket(index int, p *AVPacket) error {
	if res := C.avcodec_receive_packet(c.encoderctxs[index], p.packet); res < 0 {
		if res == AVERROR(C.EAGAIN) {
			err := c.decoder.ReadAVFrame(index, c.frame)
			if err != nil && err != io.EOF {
				return err
			}

			if res := C.avcodec_send_frame(c.encoderctxs[index], c.frame.frame); res < 0 {
				return av_err("avcodec_send_frame", res)
			}

			// try again.
			return c.ReadAVPacket(index, p)
		}
		C.avcodec_free_context(&c.encoderctxs[index])
		if err := c.frame.Close(); err != nil {
			return err
		}
		return av_err("avcodec_receive_packet", res)
	}
	return nil
}
