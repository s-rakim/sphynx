package radio

import (
	"bufio"
	"bytes"
	"io"
	"math"
	"testing"
)

func TestNodeID(t *testing.T) {
	if NodeID(0xa1b2c3d4) != "!a1b2c3d4" || NodeID(0x10) != "!00000010" {
		t.Fatal("NodeID")
	}
	for in, want := range map[string]uint32{"!a1b2c3d4": 0xa1b2c3d4, "A1B2C3D4": 0xa1b2c3d4, " !0000abcd ": 0xabcd} {
		if got, ok := ParseNodeID(in); !ok || got != want {
			t.Errorf("ParseNodeID(%q) = %x, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "!123", "!ffffffff", "!00000000", "!a1b2c3dz", "!a1b2c3d4e"} {
		if _, ok := ParseNodeID(bad); ok {
			t.Errorf("ParseNodeID(%q) should fail", bad)
		}
	}
}

func TestProtobuf(t *testing.T) {
	rssi := int64(-101) // int32 fields carry negative numbers sign-extended
	data := pb(nil).varint(1, 1).bytes(2, []byte("hi")).fixed32(6, 0xdeadbeef)
	pkt := pb(nil).fixed32(1, 0x11223344).fixed32(2, 0x55667788).bytes(4, data).fixed32(6, 42).
		fixed32(8, math.Float32bits(-7.25)).varint(9, 1).varint(10, 1).varint(12, uint64(rssi)).
		varint(14, 1).varint(15, 3).varint(17, 1).
		bytes(99, []byte("unknown field")).fixed32(98, 1).varint(97, 1) // ignored
	p, err := parseMeshPacket(pkt)
	if err != nil {
		t.Fatal(err)
	}
	want := meshPacket{From: 0x11223344, To: 0x55667788, ID: 42, RxSNR: -7.25, RxRSSI: -101, HopLimit: 1, HopStart: 3,
		WantAck: true, ViaMQTT: true, PKI: true, Decoded: true, Port: 1, Payload: []byte("hi"), RequestID: 0xdeadbeef}
	if p.From != want.From || p.To != want.To || p.ID != want.ID || p.RxSNR != want.RxSNR || p.RxRSSI != want.RxRSSI ||
		p.HopLimit != 1 || p.HopStart != 3 || !p.WantAck || !p.ViaMQTT || !p.PKI || !p.Decoded || p.Port != 1 ||
		string(p.Payload) != "hi" || p.RequestID != 0xdeadbeef {
		t.Fatalf("parseMeshPacket = %+v", p)
	}
	if p.direct() {
		t.Fatal("a relayed packet isn't direct")
	}
	for _, bad := range [][]byte{{0x0a, 0x05, 'a'}, {0x08}, {0x00, 0x01}, {0x0b}, {0x0d, 1, 2}} {
		if _, err := pbFields(bad); err == nil {
			t.Errorf("pbFields(% x) should fail", bad)
		}
	}
}

func TestMeshReader(t *testing.T) {
	var stream []byte
	stream = append(stream, "INFO | log line from the radio\r\n"...)
	stream = append(stream, meshStart1, meshStart1, meshStart2, 0, 2, 'o', 'k') // a repeated start byte
	stream = append(stream, meshStart1, meshStart2, 0xff, 0xff)                 // impossible length: skipped
	stream = append(stream, meshStart1, meshStart2, 0, 0)                       // empty frame
	stream = append(stream, meshStart1, meshStart2, 0, 3, 'e', 'n', 'd')
	r := &meshReader{r: bufio.NewReader(bytes.NewReader(stream))}
	for _, want := range []string{"ok", "", "end"} {
		got, err := r.next()
		if err != nil || string(got) != want {
			t.Fatalf("next() = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := r.next(); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
}

func TestMeshRouting(t *testing.T) {
	m := &Meshtastic{pending: map[uint32]*meshPending{}, nodes: map[uint32]*meshNode{}, myNum: 1}
	ack := func(from uint32, reason uint64) meshPacket {
		return meshPacket{From: from, To: 1, Decoded: true, Port: portRouting, RequestID: 7, Payload: pb(nil).varint(3, reason)}
	}
	ch := make(chan Result, 1)
	m.pending[7] = &meshPending{to: 2, ch: ch}
	m.packet(ack(1, 0)) // the home radio heard it relayed: not delivered yet
	select {
	case r := <-ch:
		t.Fatalf("resolved early: %+v", r)
	default:
	}
	m.packet(ack(2, 0))
	if r := <-ch; !r.Delivered || r.Err != nil {
		t.Fatalf("ack = %+v", r)
	}
	m.pending[7] = &meshPending{to: 2, ch: ch}
	m.packet(ack(1, 5))
	if r := <-ch; r.Delivered || r.Err == nil {
		t.Fatalf("nak = %+v", r)
	}
}
