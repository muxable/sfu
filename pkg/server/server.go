package server

import (
	"net"

	"github.com/pion/webrtc/v3"
)

type TrackProducer interface {
	AcceptTrackLocal() (webrtc.TrackLocal, error)
}

type TCPServer interface {
	Serve(net.Listener) error
}

type UDPServer interface {
	Serve(*net.UDPConn) error
}