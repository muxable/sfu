package mpegts

// import "github.com/pion/webrtc/v3"

// func toRTPCodecCapabilities(p *C.AVCodecParameters) (*webrtc.RTPCodecCapability, error) {
// 	// largely taken from https://ffmpeg.org/doxygen/trunk/sdp_8c_source.html#l00509
// 	var t string
//     switch p.codec_type {
//          case C.AVMEDIA_TYPE_VIDEO   : t = "video"
//          case C.AVMEDIA_TYPE_AUDIO   : t = "audio"
//          case C.AVMEDIA_TYPE_SUBTITLE: t = "text"
//          default                 : t = "application"
//      }
//      switch (p.codec_id) {
//      case C.AV_CODEC_ID_DIRAC:
// 		 return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/VC2", t),
// 			ClockRate: 90000,
// 		 }, nil
//      case C.AV_CODEC_ID_H264:
// 		 return &webrtc.RTPCodecCapability{
// 			 MimeType: webrtc.MimeTypeH264,
// 			 ClockRate: 90000,
// 			 // sps, pps, vps sent in-band
// 			 SDPFmtpLine: "packetization-mode=1",
// 		 }, nil
//          int mode = 1;
//              mode = 0;
//          if (p.extradata_size) {
//              ret = extradata2psets(fmt, p, &config);
//              if (ret < 0)
//                  return ret;
//          }
//          av_strlcatf(buff, size, "a=rtpmap:%d H264/90000\r\n"
//                                  "a=fmtp:%d packetization-mode=1\r\n",
//                                   payload_type,
//                                   payload_type, mode, config ? config : "");

//      }
//      case C.AV_CODEC_ID_H261:
//      {
//          const char *pic_fmt = NULL;
//          /* only QCIF and CIF are specified as supported in RFC 4587 */
//          if (p.width == 176 && p.height == 144)
//              pic_fmt = "QCIF=1";
//          else if (p.width == 352 && p.height == 288)
//              pic_fmt = "CIF=1";
//          if (payload_type >= RTP_PT_PRIVATE)
//              av_strlcatf(buff, size, "a=rtpmap:%d H261/90000\r\n", payload_type);
//          if (pic_fmt)
//              av_strlcatf(buff, size, "a=fmtp:%d %s\r\n", payload_type, pic_fmt);

//      }
//      case C.AV_CODEC_ID_H263:
//      case C.AV_CODEC_ID_H263P:
//          /* a=framesize is required by 3GPP TS 26.234 (PSS). It
//           * actually specifies the maximum video size, but we only know
//           * the current size. This is required for playback on Android
//           * stagefright and on Samsung bada. */
//          if (!fmt || !fmt.oformat.priv_class ||
//              !av_opt_flag_is_set(fmt.priv_data, "rtpflags", "rfc2190") ||
//              p.codec_id == C.AV_CODEC_ID_H263P)
//          av_strlcatf(buff, size, "a=rtpmap:%d H263-2000/90000\r\n"
//                                  "a=framesize:%d %d-%d\r\n",
//                                  payload_type,
//                                  payload_type, p.width, p.height);

//      case C.AV_CODEC_ID_HEVC:
//          if (p.extradata_size) {
//              ret = extradata2psets_hevc(p, &config);
//              if (ret < 0)
//                  return ret;
//          }
//          av_strlcatf(buff, size, "a=rtpmap:%d H265/90000\r\n", payload_type);
//          if (config)
//              av_strlcatf(buff, size, "a=fmtp:%d %s\r\n",
//                                       payload_type, config);

//      case C.AV_CODEC_ID_MPEG4:
//          if (p.extradata_size) {
//              ret = extradata2config(fmt, p, &config);
//              if (ret < 0)
//                  return ret;
//          }
//          av_strlcatf(buff, size, "a=rtpmap:%d MP4V-ES/90000\r\n"
//                                  "a=fmtp:%d profile-level-id=1%s\r\n",
//                                   payload_type,
//                                   payload_type, config ? config : "");

//      case C.AV_CODEC_ID_AAC:
//          if (fmt && fmt.oformat && fmt.oformat.priv_class &&
//              av_opt_flag_is_set(fmt.priv_data, "rtpflags", "latm")) {
//              ret = latm_context2config(fmt, p, &config);
//              if (ret < 0)
//                  return ret;
//              av_strlcatf(buff, size, "a=rtpmap:%d MP4A-LATM/%d/%d\r\n"
//                                      "a=fmtp:%d profile-level-id=%d;cpresent=0;config=%s\r\n",
//                                       payload_type, p.sample_rate, p.channels,
//                                       payload_type, latm_context2profilelevel(p), config);
//          } else {
//              if (p.extradata_size) {
//                  ret = extradata2config(fmt, p, &config);
//                  if (ret < 0)
//                      return ret;
//              } else {
//                  /* FIXME: maybe we can forge config information based on the
//                   *        codec parameters...
//                   */
//                  av_log(fmt, AV_LOG_ERROR, "AAC with no global headers is currently not supported.\n");
//                  return AVERROR(ENOSYS);
//              }
//              av_strlcatf(buff, size, "a=rtpmap:%d MPEG4-GENERIC/%d/%d\r\n"
//                                      "a=fmtp:%d profile-level-id=1;"
//                                      "mode=AAC-hbr;sizelength=13;indexlength=3;"
//                                      "indexdeltalength=3%s\r\n",
//                                       payload_type, p.sample_rate, p.channels,
//                                       payload_type, config);
//          }

//      case C.AV_CODEC_ID_PCM_S16BE:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/L16", t),
// 			ClockRate: p.sample_rate,
// 			Channels: p.channels,
// 		}, nil
//      case C.AV_CODEC_ID_PCM_S24BE:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/L24", t),
// 			ClockRate: p.sample_rate,
// 			Channels: p.channels,
// 		}, nil
//      case C.AV_CODEC_ID_PCM_MULAW:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: webrtc.MimeTypePCMU,
// 			ClockRate: p.sample_rate,
// 			Channels: p.channels,
// 		}, nil
//      case C.AV_CODEC_ID_PCM_ALAW:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: webrtc.MimeTypePCMA,
// 			ClockRate: p.sample_rate,
// 			Channels: p.channels,
// 		}, nil
//      case C.AV_CODEC_ID_AMR_NB:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/AMR", t),
// 			ClockRate: p.sample_rate,
// 			Channels: p.channels,
// 			SDPFmtpLine: "octet-align=1",
// 		}, nil
//      case C.AV_CODEC_ID_AMR_WB:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/AMR-WB", t),
// 			ClockRate: p.sample_rate,
// 			Channels: p.channels,
// 			SDPFmtpLine: "octet-align=1",
// 		}, nil
//      case C.AV_CODEC_ID_VP8:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: webrtc.MimeTypeVP8,
// 			ClockRate: 90000,
// 		}, nil
//      case C.AV_CODEC_ID_VP9:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: webrtc.MimeTypeVP9,
// 			ClockRate: 90000,
// 		}, nil
//      case C.AV_CODEC_ID_MJPEG:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/JPEG", t),
// 			ClockRate: 90000,
// 		}, nil
//      case C.AV_CODEC_ID_ADPCM_G722:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/G722", t, p.bits_per_coded_sample*8),
// 			ClockRate: 8000,
// 			Channels: p.channels,
// 		}, nil
//      case C.AV_CODEC_ID_ADPCM_G726: {
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/AAL2-G726-%d", t, p.bits_per_coded_sample*8),
// 			ClockRate: p.sample_rate,
// 		}, nil
//      }
//      case C.AV_CODEC_ID_ADPCM_G726LE: {
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/G726-%d", t, p.bits_per_coded_sample*8),
// 			ClockRate: p.sample_rate,
// 		}, nil
//      }
//      case C.AV_CODEC_ID_ILBC:
// 		mode := 30
// 		if p.block_align == 38 {
// 			mode = 20
// 		}
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/iLBC", t),
// 			ClockRate: p.sample_rate,
// 			SDPFmtpLine: fmt.Sprintf("mode=%d", mode),
// 		}, nil
//      case C.AV_CODEC_ID_SPEEX:
// 		return &webrtc.RTPCodecCapability{
// 			MimeType: fmt.Sprintf("%s/speex", t),
// 			ClockRate: p.sample_rate,
// 		}, nil
//      case C.AV_CODEC_ID_OPUS:
//          /* The opus RTP draft says that all opus streams MUST be declared
//             as stereo, to avoid negotiation failures. The actual number of
//             channels can change on a packet-by-packet basis. The number of
//             channels a receiver prefers to receive or a sender plans to send
//             can be declared via fmtp parameters (both default to mono), but
//             receivers MUST be able to receive and process stereo packets. */
// 		c := &webrtc.RTPCodecCapability{
// 			MimeType: webrtc.MimeTypeOpus,
// 			ClockRate: 48000,
// 			Channels: 2,
// 		}
//          if p.channels == 2 {
// 			 c.SDPFmtpLine = "sprop-stereo=1"
//          }
// 		 return c, nil
//      }
// 	 return nil, fmt.Errorf("unsupported codec %s", p.codec_id)
// }