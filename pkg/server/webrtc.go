package server

import (
	"io"
	"net"
)

type WebRTCServer struct {
	trackCh chan *NamedTrackLocal
}

func NewWebRTCServer() *WebRTCServer {
	return &WebRTCServer{
		trackCh: make(chan *NamedTrackLocal),
	}
}

func (s *RTPServer) Serve(conn *net.UDPConn) error {
	for {
	}
}

// AcceptTrackLocal returns a track that can be sent to a peer connection.
func (s *RTPServer) AcceptTrackLocal() (*NamedTrackLocal, error) {
	ntl, ok := <-s.trackCh
	if !ok {
		return nil, io.EOF
	}
	return ntl, nil
}

var _ UDPServer = (*RTPServer)(nil)
var _ TrackProducer = (*RTPServer)(nil)
