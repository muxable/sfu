package ssrc

import (
	"fmt"
	"net"

	"github.com/pion/webrtc/v3"
)

// Since net.PacketConn expects an address in the interface, we implement an "SSRC" address.
// Totally a hack, but totally works.

type SSRCAddr webrtc.SSRC

func (SSRCAddr) Network() string {
	return "udp"
}

func (s SSRCAddr) String() string {
	return fmt.Sprintf("ssrc:%08x", uint32(s))
}

var _ net.Addr = (*SSRCAddr)(nil)

// https://tools.ietf.org/html/rfc7983
func isRTPRTCP(p []byte) bool {
	return len(p) > 0 && p[0] >= 128 && p[0] <= 191
}