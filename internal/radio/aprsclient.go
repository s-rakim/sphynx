package radio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// APRSToCall is the destination Sphynx puts on the packets it sends; the
// APZ prefix is for experimental software.
const APRSToCall = "APZSPX"

// APRS sends and receives APRS messages for one callsign, through APRS-IS
// (the internet side of APRS, where gateways pass on what radios hear)
// and/or a KISS TNC attached to a radio at home.
type APRS struct {
	Call     string // with SSID, e.g. 5Z4ABC-10
	Passcode string // APRS-IS passcode for Call; without it APRS-IS only listens
	IS       string // APRS-IS server host:port, "" for none
	KISS     string // KISS TNC host:port, "" for none
	Path     []string
	Version  string

	OnMessage func(Message)
	Logf      func(format string, args ...any)

	// Retries is when unacknowledged messages are sent again (after the
	// first try); zero means the default schedule.
	Retries []time.Duration

	mu      sync.Mutex
	links   map[string]*aprsLink
	pending map[string]chan Result
	seen    map[string]time.Time // recent messages, to drop repeats
	nextID  int
	inbox   inbox
}

type aprsLink struct {
	name     string
	mu       sync.Mutex
	conn     net.Conn
	up       bool
	verified bool
	err      string
	since    time.Time
}

func (a *APRS) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
	}
}

// Run keeps the links connected until ctx ends.
func (a *APRS) Run(ctx context.Context) {
	a.mu.Lock()
	a.links = map[string]*aprsLink{}
	a.pending = map[string]chan Result{}
	a.seen = map[string]time.Time{}
	a.nextID = rand.IntN(36 * 36)
	a.inbox = newInbox()
	var wg sync.WaitGroup
	wg.Go(func() { a.inbox.run(ctx, a.OnMessage) })
	if a.IS != "" {
		l := &aprsLink{name: "APRS-IS"}
		a.links[l.name] = l
		wg.Go(func() { a.keep(ctx, l, a.runIS) })
	}
	if a.KISS != "" {
		l := &aprsLink{name: "radio (KISS)"}
		a.links[l.name] = l
		wg.Go(func() { a.keep(ctx, l, a.runKISS) })
	}
	a.mu.Unlock()
	wg.Wait()
}

// keep reconnects a link until ctx ends.
func (a *APRS) keep(ctx context.Context, l *aprsLink, run func(context.Context, *aprsLink) error) {
	reconnect(ctx, func() error {
		err := run(ctx, l)
		l.mu.Lock()
		l.up, l.conn = false, nil
		l.mu.Unlock()
		return err
	}, func(err error) {
		l.mu.Lock()
		l.err = err.Error()
		l.mu.Unlock()
		a.logf("APRS %s: %v", l.name, err)
	})
}

func (a *APRS) runIS(ctx context.Context, l *aprsLink) error {
	c, err := dial(ctx, a.IS, "14580")
	if err != nil {
		return err
	}
	defer closeOnDone(ctx, c, nil)()
	pass := a.Passcode
	if pass == "" {
		pass = "-1"
	}
	login := fmt.Sprintf("user %s pass %s vers Sphynx %s filter g/%s\r\n", a.Call, pass, a.version(), a.Call)
	if _, err := c.Write([]byte(login)); err != nil {
		return err
	}
	l.mu.Lock()
	l.conn, l.up, l.err, l.since, l.verified = c, true, "", time.Now(), false
	l.mu.Unlock()
	a.logf("APRS-IS: connected to %s as %s", a.IS, a.Call)
	r := bufio.NewReaderSize(c, 4096)
	for {
		// Servers send a comment line every 20 seconds or so.
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "#") {
			if f := strings.Fields(line); len(f) >= 4 && f[1] == "logresp" {
				l.mu.Lock()
				l.verified = f[3] == "verified," || f[3] == "verified"
				l.mu.Unlock()
				if !l.verified && a.Passcode != "" {
					a.logf("APRS-IS: the server didn't accept the passcode for %s, so Sphynx can only listen", a.Call)
				}
			}
			continue
		}
		p, err := ParsePacket(line)
		if err != nil {
			continue
		}
		a.handle(p, "APRS-IS")
	}
}

func (a *APRS) runKISS(ctx context.Context, l *aprsLink) error {
	c, err := dial(ctx, a.KISS, "8001")
	if err != nil {
		return err
	}
	defer closeOnDone(ctx, c, nil)()
	l.mu.Lock()
	l.conn, l.up, l.err, l.since = c, true, "", time.Now()
	l.mu.Unlock()
	a.logf("APRS: connected to the radio's TNC at %s", a.KISS)
	kr := newKISSReader(c)
	for {
		f, err := kr.next()
		if err != nil {
			return err
		}
		p, err := decodeAX25(f)
		if err != nil {
			continue
		}
		a.handle(p, "radio")
	}
}

func (a *APRS) version() string {
	if a.Version == "" {
		return "dev"
	}
	return a.Version
}

// handle reads a packet heard on a link.
func (a *APRS) handle(p Packet, via string) {
	p = p.Inner()
	m, ok := parseMessage(p.Info)
	if !ok || !strings.EqualFold(m.To, a.Call) {
		return
	}
	from := strings.ToUpper(p.Src)
	if m.Ack || m.Rej {
		a.resolve(from, m.ID, m.Ack)
		return
	}
	if m.AckFor != "" {
		a.resolve(from, m.AckFor, true)
	}
	if m.ID != "" {
		// Acknowledge every copy: the sender keeps trying until one of our
		// acknowledgements reaches it.
		a.transmit(formatAck(from, m.ID))
	}
	key := from + "|" + m.ID + "|" + m.Text
	a.mu.Lock()
	for k, t := range a.seen {
		if time.Since(t) > 30*time.Minute {
			delete(a.seen, k)
		}
	}
	_, dup := a.seen[key]
	a.seen[key] = time.Now()
	a.mu.Unlock()
	if dup || strings.TrimSpace(m.Text) == "" {
		return
	}
	if !a.inbox.put(Message{Link: "aprs", From: from, Text: strings.TrimSpace(m.Text), At: time.Now(), Via: via}) {
		a.logf("APRS: dropped a message from %s, too many waiting", from)
	}
}

func (a *APRS) resolve(from, id string, ok bool) {
	a.mu.Lock()
	ch := a.pending[from+"|"+id]
	delete(a.pending, from+"|"+id)
	a.mu.Unlock()
	if ch == nil {
		return
	}
	if ok {
		ch <- Result{Delivered: true}
	} else {
		ch <- Result{Err: errors.New(from + " rejected the message")}
	}
}

// transmit sends an information field on every connected link, and
// reports how many took it.
func (a *APRS) transmit(info string) int {
	a.mu.Lock()
	links := make([]*aprsLink, 0, len(a.links))
	for _, l := range a.links {
		links = append(links, l)
	}
	a.mu.Unlock()
	sent := 0
	for _, l := range links {
		l.mu.Lock()
		c, up, verified := l.conn, l.up, l.verified
		l.mu.Unlock()
		if !up || c == nil {
			continue
		}
		var data []byte
		if l.name == "APRS-IS" {
			if !verified {
				continue
			}
			data = []byte(Packet{Src: a.Call, Dst: APRSToCall, Path: []string{"TCPIP*"}, Info: info}.String() + "\r\n")
		} else {
			ax, err := encodeAX25(Packet{Src: a.Call, Dst: APRSToCall, Path: a.Path, Info: info})
			if err != nil {
				a.logf("APRS: %v", err)
				continue
			}
			data = kissFrame(ax)
		}
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write(data); err == nil {
			sent++
		}
	}
	return sent
}

var defaultAPRSRetries = []time.Duration{30 * time.Second, 60 * time.Second, 2 * time.Minute, 4 * time.Minute}

// Send sends one message (up to MaxAPRSText characters) to a callsign and
// waits until it is acknowledged, rejected or given up on.
func (a *APRS) Send(ctx context.Context, to, text string) Result {
	to = NormalizeCall(to)
	if to == "" {
		return Result{Err: errors.New("not a callsign")}
	}
	text = CleanAPRSText(text)
	if len(text) > MaxAPRSText {
		text = text[:MaxAPRSText]
	}
	a.mu.Lock()
	if a.pending == nil {
		a.mu.Unlock()
		return Result{Err: errors.New("APRS isn't running")}
	}
	a.nextID = (a.nextID + 1) % (36 * 36 * 36)
	id := strings.ToUpper(strconv.FormatInt(int64(a.nextID), 36))
	ch := make(chan Result, 1)
	a.pending[to+"|"+id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, to+"|"+id)
		a.mu.Unlock()
	}()
	info := formatMessage(to, text, id)
	retries := a.Retries
	if len(retries) == 0 {
		retries = defaultAPRSRetries
	}
	sentOnce := false
	for try := 0; ; try++ {
		if a.transmit(info) > 0 {
			sentOnce = true
		}
		var wait time.Duration
		if try < len(retries) {
			wait = retries[try]
		} else {
			wait = retries[len(retries)-1]
		}
		select {
		case r := <-ch:
			return r
		case <-ctx.Done():
			return Result{Err: ctx.Err()}
		case <-time.After(wait):
		}
		if try >= len(retries) {
			break
		}
	}
	if !sentOnce {
		return Result{Err: errors.New("no APRS link could send it (is APRS-IS signed in with a passcode, or the radio connected?)")}
	}
	return Result{Err: fmt.Errorf("no acknowledgement from %s after %d tries", to, len(retries)+1)}
}

// Status reports each link.
func (a *APRS) Status() []LinkStatus {
	a.mu.Lock()
	links := make([]*aprsLink, 0, len(a.links))
	for _, l := range a.links {
		links = append(links, l)
	}
	a.mu.Unlock()
	var out []LinkStatus
	for _, l := range links {
		l.mu.Lock()
		s := LinkStatus{Name: l.name, Connected: l.up, Error: l.err, Since: l.since}
		if l.name == "APRS-IS" && l.up {
			if l.verified {
				s.Detail = "signed in as " + a.Call
			} else {
				s.Detail = "listening only (no valid passcode for " + a.Call + ")"
			}
		}
		l.mu.Unlock()
		out = append(out, s)
	}
	return out
}
