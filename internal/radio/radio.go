package radio

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// Message is a text message heard from a radio.
type Message struct {
	Link     string    `json:"link"`               // "meshtastic" or "aprs"
	From     string    `json:"from"`               // Meshtastic node id (!a1b2c3d4) or callsign
	FromName string    `json:"fromName,omitempty"` // the radio's own name, when known
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
	Via      string    `json:"via,omitempty"` // how it reached Sphynx: radio, APRS-IS, MQTT
	// Signal strength at the home radio, for Meshtastic messages heard
	// directly (no relays).
	SNR  float32 `json:"snr,omitempty"`
	RSSI int32   `json:"rssi,omitempty"`
	Hops int     `json:"hops,omitempty"` // relays it took, when known
	// Private: a Meshtastic direct message encrypted to the home radio's
	// own key, which only that sender could have written.
	Private bool `json:"private,omitempty"`
}

// Result is how a sent message fared.
type Result struct {
	// Delivered: the other radio acknowledged it.
	Delivered bool
	Err       error
}

// LinkStatus describes one radio connection, for the admin page.
type LinkStatus struct {
	Name      string    `json:"name"`
	Connected bool      `json:"connected"`
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"`
	Since     time.Time `json:"since,omitzero"`
}

// stream is a connection to a radio: TCP or a serial port.
type stream interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// reconnect runs connect until ctx ends, pausing longer after each
// connection that fails quickly.
func reconnect(ctx context.Context, connect func() error, failed func(error)) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := connect()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = errors.New("the other end closed the connection")
		}
		failed(err)
		if time.Since(start) > time.Minute {
			wait = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 2*time.Minute)
	}
}

func dial(ctx context.Context, addr string, defPort string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, defPort)
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, "tcp", addr)
}

// closeOnDone closes c when ctx ends, so blocked reads return. The
// function it returns closes c straight away.
func closeOnDone(ctx context.Context, c io.Closer, before func()) func() {
	stop := context.AfterFunc(ctx, func() {
		if before != nil {
			before()
		}
		c.Close()
	})
	return func() { stop(); c.Close() }
}

// inbox hands received messages to a handler in order, on a goroutine of
// its own, so a slow handler never holds up the radio link (which also
// carries the acknowledgements the handler may be waiting for).
type inbox chan Message

func newInbox() inbox { return make(inbox, 64) }

func (b inbox) run(ctx context.Context, handle func(Message)) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-b:
			if handle != nil {
				handle(m)
			}
		}
	}
}

// put queues m, and reports false when too many are waiting.
func (b inbox) put(m Message) bool {
	select {
	case b <- m:
		return true
	default:
		return false
	}
}
