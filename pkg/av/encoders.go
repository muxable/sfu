package av

var DefaultH264EncoderOptions = map[string]interface{} {
	"preset": "veryfast",
	"profile": "baseline",
	"x264opts": "bframes=0:force-cfr=1:mbtree=0:sync-lookahead=0:rc-lookahead=0",
}

var DefaultVP8EncoderOptions = map[string]interface{} {
	"deadline": "realtime",
	"cpu-used": 5,
	"crf": 20,
}

var DefaultVP9EncoderOptions = map[string]interface{} {
	"deadline": "realtime",
	"cpu-used": 5,
	"crf": 20,
}

var DefaultOpusEncoderOptions = map[string]interface{} {
	// "fec": 1,
	// "packet_loss": 0,
	// "vbr": "constrained",
}
