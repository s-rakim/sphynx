package radio

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestParsePacket(t *testing.T) {
	p, err := ParsePacket("5Z4ABC-7>APDR16,TCPIP*,qAC,T2KENYA::SPHYNX-1 :hello there{12\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Src != "5Z4ABC-7" || p.Dst != "APDR16" || !slices.Equal(p.Path, []string{"TCPIP*", "qAC", "T2KENYA"}) || p.Info != ":SPHYNX-1 :hello there{12" {
		t.Fatalf("got %+v", p)
	}
	if got := p.String(); got != "5Z4ABC-7>APDR16,TCPIP*,qAC,T2KENYA::SPHYNX-1 :hello there{12" {
		t.Fatalf("String() = %q", got)
	}
	for _, bad := range []string{"no colon here", ">APRS:x", "SRC>:x"} {
		if _, err := ParsePacket(bad); err == nil {
			t.Errorf("ParsePacket(%q) should fail", bad)
		}
	}
}

func TestThirdParty(t *testing.T) {
	p, _ := ParsePacket("IGATE>APRS,WIDE2-1:}5Z4ABC-7>APRS,TCPIP,IGATE*::SPHYNX   :hi{AB")
	in := p.Inner()
	if in.Src != "5Z4ABC-7" || in.Info != ":SPHYNX   :hi{AB" {
		t.Fatalf("Inner() = %+v", in)
	}
	// Not third-party: unchanged.
	p, _ = ParsePacket("A>B::C        :x")
	if p.Inner().Src != "A" {
		t.Fatal("Inner changed a normal packet")
	}
}

func TestParseMessage(t *testing.T) {
	tests := []struct {
		info string
		want message
		ok   bool
	}{
		{":SPHYNX   :status", message{To: "SPHYNX", Text: "status"}, true},
		{":SPHYNX-1 :wake pc 123456{7", message{To: "SPHYNX-1", Text: "wake pc 123456", ID: "7"}, true},
		{":SPHYNX-1 :ok{MM}AA", message{To: "SPHYNX-1", Text: "ok", ID: "MM", AckFor: "AA"}, true},
		{":SPHYNX-1 :ack7", message{To: "SPHYNX-1", ID: "7", Ack: true}, true},
		{":SPHYNX-1 :ACK12", message{To: "SPHYNX-1", ID: "12", Ack: true}, true},
		{":SPHYNX-1 :ackMM}AA", message{To: "SPHYNX-1", ID: "MM", Ack: true}, true},
		{":SPHYNX-1 :rej3", message{To: "SPHYNX-1", ID: "3", Rej: true}, true},
		// "acknowledged" is a word, not an ack.
		{":SPHYNX-1 :acknowledged", message{To: "SPHYNX-1", Text: "acknowledged"}, true},
		// Braces inside the text that don't end in an id stay text.
		{":SPHYNX-1 :a {b c}", message{To: "SPHYNX-1", Text: "a {b c}"}, true},
		{":SHORT:", message{}, false},
		{"!4903.50N/07201.75W-position", message{}, false},
	}
	for _, tt := range tests {
		got, ok := parseMessage(tt.info)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseMessage(%q) = %+v, %v; want %+v, %v", tt.info, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFormatMessage(t *testing.T) {
	if got := formatMessage("5Z4ABC-7", "hi", "A1"); got != ":5Z4ABC-7 :hi{A1" {
		t.Fatalf("formatMessage = %q", got)
	}
	if got := formatMessage("N0CALL", "no ack", ""); got != ":N0CALL   :no ack" {
		t.Fatalf("formatMessage = %q", got)
	}
	if got := formatAck("N0CALL", "A1"); got != ":N0CALL   :ackA1" {
		t.Fatalf("formatAck = %q", got)
	}
	m, ok := parseMessage(formatMessage("SPHYNX-10", "round trip", "ZZ9"))
	if !ok || m.To != "SPHYNX-10" || m.Text != "round trip" || m.ID != "ZZ9" {
		t.Fatalf("round trip = %+v", m)
	}
}

func TestNormalizeCall(t *testing.T) {
	for in, want := range map[string]string{
		"5z4abc":      "5Z4ABC",
		" 5Z4ABC-7 ":  "5Z4ABC-7",
		"N0CALL-0":    "N0CALL",
		"N0CALL-15":   "N0CALL-15",
		"N0CALL-16":   "",
		"TOOLONGCALL": "",
		"BAD CALL":    "",
		"":            "",
	} {
		if got := NormalizeCall(in); got != want {
			t.Errorf("NormalizeCall(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanAPRSText(t *testing.T) {
	for in, want := range map[string]string{
		"Café at 5°C – ok?": "Cafe at 5 degC - ok?",
		"a|b~c{d":           "a b c d",
		"line1\nline2":      "line1 line2",
		"emoji 🙂 here":      "emoji ? here",
		"  trimmed  ":       "trimmed",
	} {
		if got := CleanAPRSText(in); got != want {
			t.Errorf("CleanAPRSText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitText(t *testing.T) {
	got := SplitText("the quick brown fox jumps over the lazy dog", 15)
	want := []string{"the quick brown", "fox jumps over", "the lazy dog"}
	if !slices.Equal(got, want) {
		t.Fatalf("SplitText = %q", got)
	}
	// A long word is cut, but never inside a UTF-8 character.
	for _, part := range SplitText(strings.Repeat("é", 20), 7) {
		if len(part) > 7 || !strings.HasPrefix(part, "é") || strings.ContainsRune(part, '�') {
			t.Fatalf("bad part %q", part)
		}
	}
	if SplitText("   ", 10) != nil {
		t.Fatal("blank text should give no parts")
	}
	if got := CutText("héllo", 2); got != "h" {
		t.Fatalf("CutText = %q", got)
	}
}

func TestAX25(t *testing.T) {
	p := Packet{Src: "N0CALL", Dst: "APRS", Path: []string{"WIDE1-1"}, Info: ">test"}
	f, err := encodeAX25(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		'A' << 1, 'P' << 1, 'R' << 1, 'S' << 1, ' ' << 1, ' ' << 1, 0xE0, // destination, C bit
		'N' << 1, '0' << 1, 'C' << 1, 'A' << 1, 'L' << 1, 'L' << 1, 0x60, // source
		'W' << 1, 'I' << 1, 'D' << 1, 'E' << 1, '1' << 1, ' ' << 1, 0x63, // WIDE1-1, last address
		0x03, 0xF0, '>', 't', 'e', 's', 't',
	}
	if !bytes.Equal(f, want) {
		t.Fatalf("encodeAX25 = % x\nwant          % x", f, want)
	}
	back, err := decodeAX25(f)
	if err != nil || back.String() != p.String() {
		t.Fatalf("decodeAX25 = %v, %v", back, err)
	}

	// Digipeated: the H bit marks the last station that repeated it.
	p = Packet{Src: "5Z4ABC-7", Dst: "APZSPX", Path: []string{"RELAY", "WIDE2*", "WIDE2-1"}, Info: ":SPHYNX   :hi{1"}
	f, _ = encodeAX25(p)
	back, err = decodeAX25(f)
	if err != nil || back.String() != p.String() {
		t.Fatalf("decodeAX25 = %v, %v", back, err)
	}

	for _, bad := range []Packet{
		{Src: "TOOLONGX", Dst: "APRS"},
		{Src: "N0CALL-16", Dst: "APRS"},
		{Src: "N0CALL", Dst: "APRS", Path: []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"}},
	} {
		if _, err := encodeAX25(bad); err == nil {
			t.Errorf("encodeAX25(%v) should fail", bad)
		}
	}
	if _, err := decodeAX25(want[:10]); err == nil {
		t.Error("a short frame should fail")
	}
	notUI := append([]byte(nil), want...)
	notUI[21] = 0x3F
	if _, err := decodeAX25(notUI); err == nil {
		t.Error("a non-UI frame should fail")
	}
}

func TestKISS(t *testing.T) {
	frame := []byte{1, kissFEND, 2, kissFESC, 3}
	k := kissFrame(frame)
	if !bytes.Equal(k, []byte{kissFEND, 0, 1, kissFESC, kissTFEND, 2, kissFESC, kissTFESC, 3, kissFEND}) {
		t.Fatalf("kissFrame = % x", k)
	}
	var stream []byte
	stream = append(stream, "noise"...)
	stream = append(stream, kissFEND, kissFEND)          // empty frames
	stream = append(stream, kissFEND, 0x06, 9, kissFEND) // a command, not data
	stream = append(stream, k...)
	stream = append(stream, kissFrame([]byte("second"))...)
	r := newKISSReader(bytes.NewReader(stream))
	got, err := r.next()
	if err != nil || !bytes.Equal(got, frame) {
		t.Fatalf("first frame = % x, %v", got, err)
	}
	got, err = r.next()
	if err != nil || string(got) != "second" {
		t.Fatalf("second frame = %q, %v", got, err)
	}
	if _, err := r.next(); err != io.EOF {
		t.Fatalf("end = %v", err)
	}
}
