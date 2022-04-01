package cdn

import (
	"net"

	"github.com/pion/rtpio/pkg/rtpio"
)

func NewFFMPEGBinding(port int) (*Binding, error) {
	dial, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return nil, err
	}
	return &Binding{
		RTPWriter: rtpio.NewRTPWriter(dial),
	}, nil
}
