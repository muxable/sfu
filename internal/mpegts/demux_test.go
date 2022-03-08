package mpegts

import (
	"os"
	"reflect"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

func TestDemux(t *testing.T) {
	f, err := os.Open("../../test/input.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	d, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}

	ptmap := make(map[uint8][]*rtp.Packet)
	for {
		p, err := d.ReadRTP()
		if err != nil {
			break
		}
		ptmap[p.PayloadType] = append(ptmap[p.PayloadType], p)
	}

	params, err := d.RTPCodecParameters()
	if err != nil {
		t.Fatal(err)
	}

	if len(ptmap) != 2 {
		t.Errorf("expected 2 payload types, got %d", len(ptmap))
	}
	if len(ptmap[96]) == 214 {
		t.Error("expected 96 to have packets")
	}
	if len(ptmap[97]) == 192 {
		t.Error("expected 97 to have packets")
	}
	if !reflect.DeepEqual(params[0], &webrtc.RTPCodecParameters{
		PayloadType: 96,
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264,
			ClockRate: 90000,
			SDPFmtpLine: "packetization-mode=1; sprop-parameter-sets=Z/QADZGbKCg/YCIAAAMAAgAAAwBkHihTLA==,aOvjxEhE; profile-level-id=F4000D",
		},
	}) {
		t.Errorf("expected params[0] to be H264: %#v", params[0])
	}
	if !reflect.DeepEqual(params[1], &webrtc.RTPCodecParameters{
		PayloadType: 97,
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels: 2,
		},
	}) {
		t.Errorf("expected params[1] to be Opus: %#v", params[1])
	}
}