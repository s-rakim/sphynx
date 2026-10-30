package radio_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s-rakim/sphynx/internal/radio"
	"github.com/s-rakim/sphynx/internal/radio/radiotest"
)

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type received struct {
	mu   sync.Mutex
	msgs []radio.Message
}

func (r *received) add(m radio.Message) {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
}

func (r *received) list() []radio.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]radio.Message(nil), r.msgs...)
}

func TestMeshtastic(t *testing.T) {
	fake, err := radiotest.NewMeshtastic("127.0.0.1:0", 0x0badcafe, "Home base")
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	fake.AddNode(radiotest.MeshNode{Num: 0xa1b2c3d4, LongName: "Malcolm T-Echo", ShortName: "MAL"})
	fake.AddNode(radiotest.MeshNode{Num: 0x22222222, LongName: "Relay hill", Ack: "implicit"})
	fake.AddNode(radiotest.MeshNode{Num: 0x33333333, LongName: "Far away", Ack: "none"})
	fake.AddNode(radiotest.MeshNode{Num: 0x44444444, LongName: "Silent", Ack: "silent"})

	var got received
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &radio.Meshtastic{Address: fake.Addr(), OnMessage: got.add, Logf: t.Logf, AckTimeout: 300 * time.Millisecond}
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	waitFor(t, "connection", func() bool { return m.Status()[0].Connected })
	if d := m.Status()[0].Detail; d != "home radio Home base (!0badcafe)" {
		t.Fatalf("status detail = %q", d)
	}
	nodes := m.Nodes()
	if len(nodes) != 4 {
		t.Fatalf("nodes = %+v", nodes)
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.ID] = n.LongName
		if !n.Key {
			t.Errorf("%s should have a key", n.ID)
		}
	}
	if names["!a1b2c3d4"] != "Malcolm T-Echo" {
		t.Fatalf("names = %v", names)
	}

	// Direct messages to the home radio arrive; channel chat doesn't.
	if err := fake.Inject(radiotest.MeshIn{From: 0xa1b2c3d4, Text: "everyone hear me?", Broadcast: true}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Inject(radiotest.MeshIn{From: 0xa1b2c3d4, Text: "status", Private: true, SNR: 4.5}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Inject(radiotest.MeshIn{From: 0x22222222, Text: "relayed hello", Hops: 2}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two messages", func() bool { return len(got.list()) == 2 })
	msgs := got.list()
	if a := msgs[0]; a.From != "!a1b2c3d4" || a.FromName != "Malcolm T-Echo" || a.Text != "status" || !a.Private || a.Hops != 0 || a.SNR != 4.5 || a.RSSI != -80 || a.Link != "meshtastic" {
		t.Fatalf("first message = %+v", a)
	}
	if b := msgs[1]; b.From != "!22222222" || b.Text != "relayed hello" || b.Private || b.Hops != 2 || b.SNR != 0 {
		t.Fatalf("second message = %+v", b)
	}

	// Sending: acknowledged, acknowledged after a relay was heard, given up
	// on by the radio, and no answer at all.
	if r := m.Send(ctx, "!a1b2c3d4", "Online 3/4: pc phone nas"); !r.Delivered || r.Err != nil {
		t.Fatalf("send = %+v", r)
	}
	if r := m.Send(ctx, "!22222222", "via the hill"); !r.Delivered || r.Err != nil {
		t.Fatalf("send with implicit ack = %+v", r)
	}
	if r := m.Send(ctx, "!33333333", "anyone?"); r.Delivered || r.Err == nil || !strings.Contains(r.Err.Error(), "out of range") {
		t.Fatalf("send out of range = %+v", r)
	}
	if r := m.Send(ctx, "!44444444", "hello?"); r.Delivered || r.Err == nil || !strings.Contains(r.Err.Error(), "no acknowledgement") {
		t.Fatalf("send to silent = %+v", r)
	}
	if r := m.Send(ctx, "not-a-node", "x"); r.Err == nil {
		t.Fatal("bad node id should fail")
	}
	long := strings.Repeat("ü", 150)
	m.Send(ctx, "!a1b2c3d4", long)
	sent := fake.Sent()
	if len(sent) != 5 || sent[0].To != 0xa1b2c3d4 || sent[0].Text != "Online 3/4: pc phone nas" {
		t.Fatalf("sent = %+v", sent)
	}
	if last := sent[4].Text; len(last) != radio.MaxMeshtasticText || last != strings.Repeat("ü", 100) {
		t.Fatalf("long text sent as %d bytes", len(last))
	}

	// The radio restarts: Sphynx reconnects by itself.
	fake.Drop()
	waitFor(t, "disconnect", func() bool { return !m.Status()[0].Connected })
	waitFor(t, "reconnect", func() bool { return m.Status()[0].Connected })
	if r := m.Send(ctx, "!a1b2c3d4", "back"); !r.Delivered {
		t.Fatalf("send after reconnect = %+v", r)
	}
	cancel()
	<-done
}

func TestAPRSIS(t *testing.T) {
	is, err := radiotest.NewAPRSIS("127.0.0.1:0", "12345")
	if err != nil {
		t.Fatal(err)
	}
	defer is.Close()
	is.Acking("5Z4ABC-7", true)

	var got received
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &radio.APRS{Call: "SPHYNX-1", Passcode: "12345", IS: is.Addr(), Version: "test", OnMessage: got.add, Logf: t.Logf,
		Retries: []time.Duration{100 * time.Millisecond, 100 * time.Millisecond}}
	go a.Run(ctx)
	waitFor(t, "login", func() bool {
		s := a.Status()
		return len(s) == 1 && s[0].Connected && s[0].Detail == "signed in as SPHYNX-1"
	})

	// A message for Sphynx is acknowledged (every copy) and passed on once.
	for range 2 {
		if err := is.Inject("5Z4ABC-7", "SPHYNX-1", "status", "42"); err != nil {
			t.Fatal(err)
		}
	}
	if err := is.Inject("5Z4ABC-7", "OTHER", "not for sphynx", "43"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two acks", func() bool {
		n := 0
		for _, l := range is.Lines() {
			if strings.HasSuffix(l, "::5Z4ABC-7 :ack42") {
				n++
			}
		}
		return n == 2
	})
	waitFor(t, "the message", func() bool { return len(got.list()) == 1 })
	if m := got.list()[0]; m.From != "5Z4ABC-7" || m.Text != "status" || m.Via != "APRS-IS" || m.Link != "aprs" {
		t.Fatalf("message = %+v", m)
	}
	if !strings.HasPrefix(is.Lines()[0], "SPHYNX-1>APZSPX,TCPIP*::5Z4ABC-7") {
		t.Fatalf("ack line = %q", is.Lines()[0])
	}

	// Sending: acknowledged by an acking station; retried, then given up
	// for a station that never answers.
	if r := a.Send(ctx, "5z4abc-7", "Online 3/4"); !r.Delivered || r.Err != nil {
		t.Fatalf("send = %+v", r)
	}
	before := len(is.Lines())
	r := a.Send(ctx, "N0CALL", "hello")
	if r.Delivered || r.Err == nil || !strings.Contains(r.Err.Error(), "after 3 tries") {
		t.Fatalf("send to silent station = %+v", r)
	}
	if n := len(is.Lines()) - before; n != 3 {
		t.Fatalf("sent %d copies, want 3", n)
	}
	if r := a.Send(ctx, "not a call", "x"); r.Err == nil {
		t.Fatal("bad callsign should fail")
	}
}

func TestAPRSISListenOnly(t *testing.T) {
	is, err := radiotest.NewAPRSIS("127.0.0.1:0", "12345")
	if err != nil {
		t.Fatal(err)
	}
	defer is.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &radio.APRS{Call: "SPHYNX-1", Passcode: "999", IS: is.Addr(), Logf: t.Logf, Retries: []time.Duration{50 * time.Millisecond}}
	go a.Run(ctx)
	waitFor(t, "login", func() bool { s := a.Status(); return len(s) == 1 && strings.HasPrefix(s[0].Detail, "listening only") })
	if r := a.Send(ctx, "5Z4ABC-7", "hi"); r.Err == nil || !strings.Contains(r.Err.Error(), "no APRS link could send it") {
		t.Fatalf("send = %+v", r)
	}
	if len(is.Lines()) != 0 {
		t.Fatalf("an unverified login sent %q", is.Lines())
	}
}

func TestAPRSKISS(t *testing.T) {
	tnc, err := radiotest.NewKISS("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tnc.Close()
	var got received
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &radio.APRS{Call: "SPHYNX-1", KISS: tnc.Addr(), Path: []string{"WIDE1-1", "WIDE2-1"}, OnMessage: got.add, Logf: t.Logf,
		Retries: []time.Duration{100 * time.Millisecond}}
	go a.Run(ctx)
	waitFor(t, "TNC connection", tnc.Connected)
	waitFor(t, "link up", func() bool { s := a.Status(); return len(s) == 1 && s[0].Connected })

	// Heard on the air, through a digipeater.
	frame := radiotest.AX25("5Z4ABC-7", "APDR16", []string{"RELAY*", "WIDE2-1"}, ":SPHYNX-1 :ping{9")
	if err := tnc.Inject(frame); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message", func() bool { return len(got.list()) == 1 })
	if m := got.list()[0]; m.From != "5Z4ABC-7" || m.Text != "ping" || m.Via != "radio" {
		t.Fatalf("message = %+v", m)
	}
	waitFor(t, "the ack on air", func() bool { return len(tnc.Frames()) == 1 })
	if s := radiotest.DecodeAX25(tnc.Frames()[0]); s != "SPHYNX-1>APZSPX,WIDE1-1,WIDE2-1::5Z4ABC-7 :ack9" {
		t.Fatalf("ack frame = %q", s)
	}

	// A message out, acknowledged by the other station over the air.
	res := make(chan radio.Result, 1)
	go func() { res <- a.Send(ctx, "5Z4ABC-7", "pong") }()
	waitFor(t, "the message on air", func() bool { return len(tnc.Frames()) == 2 })
	sent := radiotest.DecodeAX25(tnc.Frames()[1])
	_, id, _ := strings.Cut(sent, "{")
	if !strings.HasPrefix(sent, "SPHYNX-1>APZSPX,WIDE1-1,WIDE2-1::5Z4ABC-7 :pong{") || id == "" {
		t.Fatalf("sent %q", sent)
	}
	if err := tnc.Inject(radiotest.AX25("5Z4ABC-7", "APDR16", nil, ":SPHYNX-1 :ack"+id)); err != nil {
		t.Fatal(err)
	}
	if r := <-res; !r.Delivered {
		t.Fatalf("send = %+v", r)
	}
}
