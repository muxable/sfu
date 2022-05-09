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

type FilterContext struct {
	buffersrcctx    *C.AVFilterContext
	buffersinkctx   *C.AVFilterContext
	filtergraph     *C.AVFilterGraph
	frame           *AVFrame
	Sink            *IndexedSink
	requestKeyframe bool
}

type FilterConfiguration struct {
	Name           string // if unset, encoder will resolve by output type.
	InitialBitrate int64
	Codec          webrtc.RTPCodecCapability
	Options        map[string]interface{}
}

func NewFilter(desc string) (*FilterContext, error) {
	cdescr := C.CString(desc)
	defer C.free(unsafe.Pointer(cdescr))

	buffersrc := C.avfilter_get_by_name("buffer")
	if buffersrc == nil {
		return nil, errors.New("failed to find buffer filter")
	}
	buffersink := C.avfilter_get_by_name("buffersink")
	if buffersink == nil {
		return nil, errors.New("failed to find buffersink filter")
	}
	outputs := C.avfilter_inout_alloc()
	inputs := C.avfilter_inout_alloc()
	if outputs == nil || inputs == nil {
		return nil, errors.New("failed to allocate filter inout")
	}

	var buffersinkctx *C.AVFilterContext
	var buffersrcctx *C.AVFilterContext
	
    filtergraph = C.avfilter_graph_alloc()

    snprintf(args, sizeof(args),
            "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
            dec_ctx->width, dec_ctx->height, dec_ctx->pix_fmt,
            dec_ctx->time_base.num, dec_ctx->time_base.den,
            dec_ctx->sample_aspect_ratio.num, dec_ctx->sample_aspect_ratio.den);
    if averr := C.avfilter_graph_create_filter(buffersrc_ctx, buffersrc, "in",
                                       args, nil, filtergraph); averr < 0 {
		return nil, av_err("avfilter_graph_create_filter", averr)
	}

    buffersink_params := C.av_buffersink_params_alloc()
	defer C.av_free(unsafe.Pointer(buffersink_params))

    buffersink_params.pixel_fmts = pix_fmts

    if averr := C.avfilter_graph_create_filter(buffersinkctx, buffersink, "out", nil, buffersink_params, filter_graph); averr < 0 {
		return nil, av_err("avfilter_graph_create_filter", averr)
    }
    outputs.name       = av_strdup("in")
    outputs.filter_ctx = buffersrc_ctx
    outputs.pad_idx    = 0
    outputs.next       = nil
    inputs.name       = av_strdup("out")
    inputs.filter_ctx = buffersink_ctx
    inputs.pad_idx    = 0
    inputs.next       = nil
	
    if averr := C.avfilter_graph_parse_ptr(filtergraph, cdescr, &inputs, &outputs, nil)) < 0; averr < 0 {
		return nil, av_err("avfilter_graph_parse_ptr", averr)
	}
    if averr := C.avfilter_graph_config(filtergraph, nil)) < 0; averr < 0 {
		return nil, av_err("avfilter_graph_config", averr)
	}

	return &FilterContext{
		buffersrcctx: buffersrcctx,
		frame:        NewAVFrame(),
	}, nil
}

func (c *FilterContext) WriteAVFrame(f *AVFrame) error {
	if res := C.av_buffersrc_add_frame_flags(c.buffersrcctx, f.frame, C.AV_BUFFERSRC_FLAG_KEEP_REF); res < 0 {
		return av_err("avcodec_send_frame", res)
	}

	for {
		if res := C.av_buffersink_get_frame(c.buffersinkctx, c.frame.frame); res < 0 {
			if res == AVERROR(C.EAGAIN) {
				return nil
			}
			return av_err("failed to receive frame", res)
		}

		if sink := c.Sink; sink != nil {
			if err := sink.WriteAVFrame(c.frame); err != nil {
				return err
			}
		}
		C.av_frame_unref(c.frame.frame)
	}
}

func (c *FilterContext) Close() error {
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
	C.avfilter_graph_free(&c.filtergraph)

	return nil
}
