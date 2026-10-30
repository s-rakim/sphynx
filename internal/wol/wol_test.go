package wol

import (
	"bytes"
	"testing"
)

func TestParseMAC(t *testing.T) {
	for _, in := range []string{"3c:7c:3f:12:34:56", "3C-7C-3F-12-34-56", "3c7c3f123456", " 3c:7c:3f:12:34:56 "} {
		mac, err := ParseMAC(in)
		if err != nil {
			t.Errorf("ParseMAC(%q): %v", in, err)
			continue
		}
		if got := mac.String(); got != "3c:7c:3f:12:34:56" {
			t.Errorf("ParseMAC(%q) = %s", in, got)
		}
	}
	for _, in := range []string{"", "3c:7c:3f", "not a mac", "02:00:5e:10:00:00:00:01"} {
		if _, err := ParseMAC(in); err == nil {
			t.Errorf("ParseMAC(%q) succeeded, want error", in)
		}
	}
}

func TestMagicPacket(t *testing.T) {
	mac, _ := ParseMAC("aa:bb:cc:dd:ee:ff")
	p := MagicPacket(mac)
	if len(p) != 102 {
		t.Fatalf("len = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) {
		t.Errorf("sync stream = % x", p[:6])
	}
	if !bytes.Equal(p[6:], bytes.Repeat(mac, 16)) {
		t.Error("MAC is not repeated 16 times")
	}
}
