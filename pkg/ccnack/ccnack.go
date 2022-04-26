package ccnack

import (
	"log"

	"github.com/pion/interceptor"
)

const (
	transportCCURI = "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"
)

func streamSupportCcnack(info *interceptor.StreamInfo) bool {
	log.Printf("streamSupportCcnack: %v", info)
	for _, fb := range info.RTCPFeedback {
		if fb.Type == "ccnack" && fb.Parameter == "" {
			return true
		}
	}

	return false
}