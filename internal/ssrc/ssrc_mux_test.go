package ssrc

import (
	"bytes"
	"net"
	"testing"

	"github.com/pion/rtp"
	"go.uber.org/goleak"
)

func TestSSRCConn_BasicConnectivity(t *testing.T) {
	defer goleak.VerifyNone(t)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 4444})
	if err != nil {
		t.Fatal(err)
	}

	ssrcConn, err := NewSSRCMux(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer ssrcConn.Close()

	dial, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial.Close()

	// Send a packet
	if _, err := dial.Write([]byte{0x01, 0x02, 0x03, 0x04}); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	buf := make([]byte, 1500)
	n, sender, err := ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 bytes, got %d", n)
	}
	if !bytes.Equal(buf[:n], []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("expected %v, got %v", []byte{0x01, 0x02, 0x03, 0x04}, buf[:n])
	}

	// Send a reply
	if _, err := ssrcConn.WriteTo([]byte{0x01, 0x02, 0x03, 0x04}, sender); err != nil {
		t.Fatal(err)
	}

	// Receive a reply
	n, err = dial.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 bytes, got %d", n)
	}
	if !bytes.Equal(buf[:n], []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("expected %v, got %v", []byte{0x01, 0x02, 0x03, 0x04}, buf[:n])
	}
}

func TestSSRCConn_TwoSenders_SameSSRC(t *testing.T) {
	defer goleak.VerifyNone(t)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 4444})
	if err != nil {
		t.Fatal(err)
	}

	ssrcConn, err := NewSSRCMux(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer ssrcConn.Close()

	dial1, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial1.Close()

	dial2, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial2.Close()

	p := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 0x12345678}}

	pbuf, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	// Send a packet
	if _, err := dial1.Write(pbuf); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	buf := make([]byte, 1500)
	n, sender, err := ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x12345678) {
		t.Fatalf("expected SSRC 0x12345678, got %d", sender.(SSRCAddr))
	}

	// Send a packet from another connection
	if _, err := dial2.Write(pbuf); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	n, err = ssrcConn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x12345678) {
		t.Fatalf("expected SSRC 0x12345678, got %d", sender.(SSRCAddr))
	}
}

func TestSSRCConn_TwoSenders_DifferentSSRC(t *testing.T) {
	defer goleak.VerifyNone(t)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 4444})
	if err != nil {
		t.Fatal(err)
	}

	ssrcConn, err := NewSSRCMux(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer ssrcConn.Close()

	dial1, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial1.Close()

	dial2, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial2.Close()

	p1 := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 0x12345678}}

	pbuf1, err := p1.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	p2 := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 0x87654321}}

	pbuf2, err := p2.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	// Send a packet
	if _, err := dial1.Write(pbuf1); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	buf := make([]byte, 1500)
	n, sender, err := ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf1) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf1), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x12345678) {
		t.Fatalf("expected SSRC 0x12345678, got %d", sender.(SSRCAddr))
	}

	// Send a packet from another connection
	if _, err := dial2.Write(pbuf2); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	n, sender, err = ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf2) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf2), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x87654321) {
		t.Fatalf("expected SSRC 0x87654321, got %d", sender.(SSRCAddr))
	}
}

func TestSSRCConn_TwoSenders_ReplyToLatest(t *testing.T) {
	defer goleak.VerifyNone(t)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 4444})
	if err != nil {
		t.Fatal(err)
	}

	ssrcConn, err := NewSSRCMux(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer ssrcConn.Close()

	dial1, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial1.Close()

	dial2, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444})
	if err != nil {
		t.Fatal(err)
	}
	defer dial2.Close()

	p := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 0x12345678}}

	pbuf, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	// Send a packet
	if _, err := dial1.Write(pbuf); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	buf := make([]byte, 1500)
	n, sender, err := ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x12345678) {
		t.Fatalf("expected SSRC 0x12345678, got %d", sender.(SSRCAddr))
	}

	// Send a packet from another connection
	if _, err := dial2.Write(pbuf); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	n, sender, err = ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x12345678) {
		t.Fatalf("expected SSRC 0x12345678, got %d", sender.(SSRCAddr))
	}

	// Send a reply
	if _, err := ssrcConn.WriteTo([]byte{0x01, 0x02, 0x03, 0x04}, sender); err != nil {
		t.Fatal(err)
	}

	// Receive the packet from dial2
	n, err = dial2.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 bytes, got %d", n)
	}
	if !bytes.Equal(buf[:n], []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("expected %v, got %v", []byte{0x01, 0x02, 0x03, 0x04}, buf[:n])
	}

	// Send a packet from another connection
	if _, err := dial1.Write(pbuf); err != nil {
		t.Fatal(err)
	}

	// Receive a packet
	n, sender, err = ssrcConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(pbuf) {
		t.Fatalf("expected %d bytes, got %d", len(pbuf), n)
	}
	if sender.(SSRCAddr) != SSRCAddr(0x12345678) {
		t.Fatalf("expected SSRC 0x12345678, got %d", sender.(SSRCAddr))
	}

	// Send a reply
	if _, err := ssrcConn.WriteTo([]byte{0x01, 0x02, 0x03, 0x04}, sender); err != nil {
		t.Fatal(err)
	}

	// Receive the packet from dial2
	n, err = dial1.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 bytes, got %d", n)
	}
	if !bytes.Equal(buf[:n], []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("expected %v, got %v", []byte{0x01, 0x02, 0x03, 0x04}, buf[:n])
	}
}
