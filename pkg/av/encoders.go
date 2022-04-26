package av

var DefaultH264EncoderOptions = map[string]interface{} {
	"preset": "ultrafast",
	"level": "4.0",
	"profile": "baseline",
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

var DefaultOpusEncoderOptions = map[string]interface{} {}
