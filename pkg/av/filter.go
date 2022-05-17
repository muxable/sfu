package av

/*
#cgo pkg-config: libavfilter libavutil
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/channel_layout.h>
#include <libavutil/opt.h>
#include <libavutil/pixdesc.h>
*/
import "C"
import (
	"errors"
	"fmt"
	"log"
	"unsafe"
)

type FilterContext struct {
	buffersrcctx  *C.AVFilterContext
	buffersinkctx *C.AVFilterContext
	filtergraph   *C.AVFilterGraph
	Sink          AVFrameWriteCloser
}

var (
	cbuffer         = C.CString("buffer")
	cbuffersink     = C.CString("buffersink")
	cabuffer        = C.CString("abuffer")
	cabuffersink    = C.CString("abuffersink")
	cin             = C.CString("in")
	cout            = C.CString("out")
	csamplefmts     = C.CString("sample_fmts")
	csamplerates    = C.CString("sample_rates")
	cchannellayouts = C.CString("channel_layouts")
)

func NewFilter(decoder *DecodeContext, encoder *EncodeContext) (*FilterContext, error) {
	filtergraph := C.avfilter_graph_alloc()
	if filtergraph == nil {
		return nil, errors.New("failed to allocate filter graph")
	}

	outputs := C.avfilter_inout_alloc()
	inputs := C.avfilter_inout_alloc()
	if outputs == nil || inputs == nil {
		return nil, errors.New("failed to allocate filter inout")
	}
	defer C.avfilter_inout_free(&inputs)
	defer C.avfilter_inout_free(&outputs)

	var buffersinkctx *C.AVFilterContext
	var buffersrcctx *C.AVFilterContext

	decctx := decoder.decoderctx
	encctx := encoder.encoderctx

	if decctx.codec_type != encctx.codec_type {
		return nil, errors.New("codec types do not match")
	}
	switch decctx.codec_type {
	case C.AVMEDIA_TYPE_VIDEO:
		buffersrc := C.avfilter_get_by_name(cbuffer)
		if buffersrc == nil {
			return nil, errors.New("failed to find buffer filter")
		}
		buffersink := C.avfilter_get_by_name(cbuffersink)
		if buffersink == nil {
			return nil, errors.New("failed to find buffersink filter")
		}

		decdesc := fmt.Sprintf(
			"video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
			decctx.width, decctx.height, decctx.pix_fmt,
			decctx.time_base.num, decctx.time_base.den,
			decctx.sample_aspect_ratio.num, decctx.sample_aspect_ratio.den)

		log.Printf("%v", decdesc)

		cdecdesc := C.CString(decdesc)
		defer C.free(unsafe.Pointer(cdecdesc))

		if averr := C.avfilter_graph_create_filter(&buffersrcctx, buffersrc, cin, cdecdesc, nil, filtergraph); averr < 0 {
			return nil, av_err("avfilter_graph_create_filter", averr)
		}

		if averr := C.avfilter_graph_create_filter(&buffersinkctx, buffersink, cout, nil, nil, filtergraph); averr < 0 {
			return nil, av_err("avfilter_graph_create_filter", averr)
		}

		if averr := C.av_opt_set_bin(
			unsafe.Pointer(buffersinkctx), C.CString("pix_fmts"),
			(*C.uint8_t)(unsafe.Pointer(&encctx.pix_fmt)), 4, C.AV_OPT_SEARCH_CHILDREN); averr < 0 {
			return nil, av_err("av_opt_set_bin", averr)
		}

		outputs.name = C.av_strdup(cin)
		outputs.filter_ctx = buffersrcctx
		outputs.pad_idx = 0
		outputs.next = nil
		inputs.name = C.av_strdup(cout)
		inputs.filter_ctx = buffersinkctx
		inputs.pad_idx = 0
		inputs.next = nil

		filterdesc := fmt.Sprintf(
			"fps=%d/%d,format=pix_fmts=%s,scale=w=%d:h=%d",
			encctx.framerate.num, encctx.framerate.den, C.GoString(C.av_get_pix_fmt_name(encctx.pix_fmt)),
			encctx.width, encctx.height)

		cfilterdesc := C.CString(filterdesc)
		defer C.free(unsafe.Pointer(cfilterdesc))

		if averr := C.avfilter_graph_parse_ptr(filtergraph, cfilterdesc, &inputs, &outputs, nil); averr < 0 {
			return nil, av_err("avfilter_graph_parse_ptr video", averr)
		}
		if averr := C.avfilter_graph_config(filtergraph, nil); averr < 0 {
			return nil, av_err("avfilter_graph_config", averr)
		}

	case C.AVMEDIA_TYPE_AUDIO:
		buffersrc := C.avfilter_get_by_name(cabuffer)
		if buffersrc == nil {
			return nil, errors.New("failed to find buffer filter")
		}
		buffersink := C.avfilter_get_by_name(cabuffersink)
		if buffersink == nil {
			return nil, errors.New("failed to find buffersink filter")
		}

		if decctx.channel_layout == 0 {
			decctx.channel_layout = C.ulong(C.av_get_default_channel_layout(decctx.channels))
		}
		decdesc := fmt.Sprintf(
			"time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channel_layout=0x%016x",
			decctx.time_base.num, decctx.time_base.den, decctx.sample_rate,
			C.GoString(C.av_get_sample_fmt_name(decctx.sample_fmt)),
			decctx.channel_layout)

		cdecdesc := C.CString(decdesc)
		defer C.free(unsafe.Pointer(cdecdesc))

		if averr := C.avfilter_graph_create_filter(&buffersrcctx, buffersrc, cin, cdecdesc, nil, filtergraph); averr < 0 {
			return nil, av_err("avfilter_graph_create_filter", averr)
		}

		if averr := C.avfilter_graph_create_filter(&buffersinkctx, buffersink, cout, nil, nil, filtergraph); averr < 0 {
			return nil, av_err("avfilter_graph_create_filter", averr)
		}

		if averr := C.av_opt_set_bin(
			unsafe.Pointer(buffersinkctx), csamplefmts,
			(*C.uint8_t)(unsafe.Pointer(&encctx.sample_fmt)), 4, C.AV_OPT_SEARCH_CHILDREN); averr < 0 {
			return nil, av_err("av_opt_set_bin", averr)
		}
		if averr := C.av_opt_set_bin(
			unsafe.Pointer(buffersinkctx), cchannellayouts,
			(*C.uint8_t)(unsafe.Pointer(&encctx.channel_layout)), 8, C.AV_OPT_SEARCH_CHILDREN); averr < 0 {
			return nil, av_err("av_opt_set_bin", averr)
		}
		if averr := C.av_opt_set_bin(
			unsafe.Pointer(buffersinkctx), csamplerates,
			(*C.uint8_t)(unsafe.Pointer(&encctx.sample_rate)), 4, C.AV_OPT_SEARCH_CHILDREN); averr < 0 {
			return nil, av_err("av_opt_set_bin", averr)
		}

		outputs.name = C.av_strdup(cin)
		outputs.filter_ctx = buffersrcctx
		outputs.pad_idx = 0
		outputs.next = nil
		inputs.name = C.av_strdup(cout)
		inputs.filter_ctx = buffersinkctx
		inputs.pad_idx = 0
		inputs.next = nil

		// this can actually be anull, but we leave it here as it's a nice demonstration of a filter.
		// TODO: is there any performance benefit?
		filterdesc := fmt.Sprintf(
			"aformat=sample_rates=%d:sample_fmts=%s:channel_layouts=0x%016x",
			encctx.sample_rate, C.GoString(C.av_get_sample_fmt_name(encctx.sample_fmt)),
			encctx.channel_layout)

		cfilterdesc := C.CString(filterdesc)
		defer C.free(unsafe.Pointer(cfilterdesc))

		if averr := C.avfilter_graph_parse_ptr(filtergraph, cfilterdesc, &inputs, &outputs, nil); averr < 0 {
			return nil, av_err("avfilter_graph_parse_ptr", averr)
		}
		if averr := C.avfilter_graph_config(filtergraph, nil); averr < 0 {
			return nil, av_err("avfilter_graph_config", averr)
		}

		C.av_buffersink_set_frame_size(buffersinkctx, C.uint(encctx.frame_size))
	}

	return &FilterContext{
		buffersrcctx:  buffersrcctx,
		buffersinkctx: buffersinkctx,
		filtergraph:   filtergraph,
	}, nil
}

func (c *FilterContext) WriteAVFrame(f *AVFrame) error {
	if res := C.av_buffersrc_add_frame(c.buffersrcctx, f.frame); res < 0 {
		return av_err("avcodec_send_frame", res)
	}

	for {
		g := NewAVFrame()
		if res := C.av_buffersink_get_frame(c.buffersinkctx, g.frame); res < 0 {
			if res == AVERROR(C.EAGAIN) {
				return nil
			}
			return av_err("failed to receive frame", res)
		}

		g.frame.pts = g.frame.best_effort_timestamp

		if sink := c.Sink; sink != nil {
			if err := sink.WriteAVFrame(g); err != nil {
				return err
			}
		}
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

	// free the context
	C.avfilter_graph_free(&c.filtergraph)

	return nil
}
