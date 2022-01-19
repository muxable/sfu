package clock

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/pion/rtcp"

	"github.com/muxable/rtptools/pkg/x_time"
)

var errInvalidReport = errors.New("invalid report")

type SenderClock struct {
	SenderNTPTime uint32
}

func (s SenderClock) Marshal() ([]byte, error) {
	rawPacket := make([]byte, 4)
	binary.BigEndian.PutUint32(rawPacket, s.SenderNTPTime)
	return rawPacket, nil
}

func (s *SenderClock) Unmarshal(rawPacket []byte) error {
	if len(rawPacket) != 4 {
		return errInvalidReport
	}
	s.SenderNTPTime = binary.BigEndian.Uint32(rawPacket)
	return nil
}

func NewSenderClockRawPacket(ts time.Time) (rtcp.RawPacket, error) {
	senderClock := SenderClock{SenderNTPTime: uint32(x_time.GoTimeToNTP(ts) >> 16)}
	payload, err := senderClock.Marshal()
	if err != nil {
		return nil, err
	}
	header := rtcp.Header{
		Padding: false,
		Count:   29,
		Type:    rtcp.TypeTransportSpecificFeedback,
		Length:  uint16(len(payload) / 4),
	}
	hData, err := header.Marshal()
	if err != nil {
		return nil, err

	}
	buf := make([]byte, len(payload)+len(hData))
	copy(buf, hData)
	copy(buf[len(hData):], payload)
	return rtcp.RawPacket(buf), nil
}

type ReceiverClock struct {
	LastSenderNTPTime uint32
	Delay             uint32
}

func (r ReceiverClock) Marshal() ([]byte, error) {
	rawPacket := make([]byte, 8)
	binary.BigEndian.PutUint32(rawPacket, r.LastSenderNTPTime)
	binary.BigEndian.PutUint32(rawPacket[4:], r.Delay)
	return rawPacket, nil
}

func (r *ReceiverClock) Unmarshal(rawPacket []byte) error {
	if len(rawPacket) != 8 {
		return errInvalidReport
	}
	r.LastSenderNTPTime = binary.BigEndian.Uint32(rawPacket)
	r.Delay = binary.BigEndian.Uint32(rawPacket[4:])
	return nil
}
