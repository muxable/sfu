package ssrc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
)

type BondingRequest struct {
	key uint32
	ntp uint32
}

var errInvalidReport = errors.New("invalid report")

var magicHeader = []byte("mugitcow")

func (r *BondingRequest) Marshal() ([]byte, error) {
	rawPacket := make([]byte, 16)
	copy(rawPacket, magicHeader)
	binary.BigEndian.PutUint32(rawPacket[8:], r.key)
	binary.BigEndian.PutUint32(rawPacket[12:], r.ntp)
	return rawPacket, nil
}

func (r *BondingRequest) Unmarshal(rawPacket []byte) error {
	if len(rawPacket) != 16 || !bytes.Equal(rawPacket[:8], magicHeader) {
		return errInvalidReport
	}
	r.key = binary.BigEndian.Uint32(rawPacket[8:])
	r.ntp = binary.BigEndian.Uint32(rawPacket[12:])
	return nil
}

func (r *BondingRequest) KeyAddr() *net.UDPAddr {
	ip := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint32(ip, r.key)
	return &net.UDPAddr{IP: net.IP(ip)}
}

type BondingResponse struct {
	ntp   uint32
	delay uint32
}

func (r *BondingResponse) Marshal() ([]byte, error) {
	rawPacket := make([]byte, 16)
	copy(rawPacket, magicHeader)
	binary.BigEndian.PutUint32(rawPacket[8:], r.ntp)
	binary.BigEndian.PutUint32(rawPacket[12:], r.delay)
	return rawPacket, nil
}

func (r *BondingResponse) Unmarshal(rawPacket []byte) error {
	if len(rawPacket) != 16 || !bytes.Equal(rawPacket[:8], magicHeader) {
		return errInvalidReport
	}
	r.ntp = binary.BigEndian.Uint32(rawPacket[8:])
	r.delay = binary.BigEndian.Uint32(rawPacket[12:])
	return nil
}
