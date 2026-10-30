package radio

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Meshtastic talks to a Meshtastic radio through the client API its phone
// apps use: over TCP for radios with Wi-Fi or Ethernet (port 4403), or a
// USB serial port. Each message is a protobuf in a small frame (0x94 0xC3,
// a 2-byte length, the bytes); only the fields Sphynx needs are read and
// written, by their numbers in the published client API.
type Meshtastic struct {
	// Address is host[:port] of a radio on the network, or the path of a
	// USB serial port such as /dev/ttyUSB0.
	Address string

	OnMessage func(Message)
	Logf      func(format string, args ...any)

	// AckTimeout is how long Send waits for the other radio's
	// acknowledgement; zero means 3 minutes.
	AckTimeout time.Duration

	mu       sync.Mutex
	write    func([]byte) error // nil while disconnected
	ready    bool               // configuration downloaded
	err      string
	since    time.Time
	myNum    uint32
	hopLimit uint32
	nodes    map[uint32]*meshNode
	pending  map[uint32]*meshPending
	seen     map[uint64]time.Time
	inbox    inbox
}

type meshNode struct {
	num       uint32
	longName  string
	shortName string
	lastHeard time.Time
	snr       float32
	direct    bool // last heard without relays
	hasKey    bool
}

type meshPending struct {
	to uint32
	ch chan Result
}

// Node is a radio the home radio knows about.
type Node struct {
	ID        string    `json:"id"`
	LongName  string    `json:"longName,omitempty"`
	ShortName string    `json:"shortName,omitempty"`
	LastHeard time.Time `json:"lastHeard,omitzero"`
	SNR       float32   `json:"snr,omitempty"`
	Direct    bool      `json:"direct,omitempty"`
	// Key: the home radio has its public key, so direct messages between
	// them are encrypted to each other.
	Key bool `json:"key,omitempty"`
}

const (
	meshStart1   = 0x94
	meshStart2   = 0xC3
	meshMaxFrame = 512

	meshBroadcast = 0xFFFFFFFF

	portText     = 1
	portNodeInfo = 4
	portRouting  = 5

	// MaxMeshtasticText is the longest text Sphynx puts in one Meshtastic
	// message, the same limit as the Meshtastic apps.
	MaxMeshtasticText = 200

	maxMeshNodes = 1000
)

// NodeID formats a node number the way Meshtastic shows it.
func NodeID(num uint32) string { return fmt.Sprintf("!%08x", num) }

// ParseNodeID reads a node id like !a1b2c3d4.
func ParseNodeID(s string) (uint32, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "!")
	if len(s) != 8 {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil || n == 0 || n == meshBroadcast {
		return 0, false
	}
	return uint32(n), true
}

func (m *Meshtastic) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// Run keeps the radio connected until ctx ends.
func (m *Meshtastic) Run(ctx context.Context) {
	m.mu.Lock()
	m.nodes = map[uint32]*meshNode{}
	m.pending = map[uint32]*meshPending{}
	m.seen = map[uint64]time.Time{}
	m.inbox = newInbox()
	m.mu.Unlock()
	var wg sync.WaitGroup
	wg.Go(func() { m.inbox.run(ctx, m.OnMessage) })
	reconnect(ctx, func() error { return m.session(ctx) }, func(err error) {
		m.mu.Lock()
		m.err = err.Error()
		m.mu.Unlock()
		m.logf("Meshtastic: %v", err)
	})
	wg.Wait()
}

func (m *Meshtastic) open(ctx context.Context) (stream, error) {
	if strings.HasPrefix(m.Address, "/dev/") {
		return openSerial(m.Address)
	}
	return dial(ctx, m.Address, "4403")
}

// session is one connection to the radio.
func (m *Meshtastic) session(ctx context.Context) error {
	c, err := m.open(ctx)
	if err != nil {
		return err
	}
	var wmu sync.Mutex
	write := func(msg []byte) error {
		frame := append([]byte{meshStart1, meshStart2, byte(len(msg) >> 8), byte(len(msg))}, msg...)
		wmu.Lock()
		defer wmu.Unlock()
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := c.Write(frame)
		return err
	}
	// Say goodbye when Sphynx stops, so the radio frees the connection.
	defer closeOnDone(ctx, c, func() { _ = write(pb(nil).varint(4, 1)) })()
	defer func() {
		m.mu.Lock()
		m.write, m.ready = nil, false
		m.mu.Unlock()
	}()

	// Wake the radio's API (on serial it may be printing its log), then ask
	// for its configuration: who it is, the radios it knows, its settings.
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(bytes.Repeat([]byte{meshStart2}, 32)); err != nil {
		return err
	}
	configID := rand.Uint32() | 1
	if err := write(pb(nil).varint(3, uint64(configID))); err != nil {
		return err
	}
	m.mu.Lock()
	m.write, m.hopLimit = write, 0
	m.mu.Unlock()

	r := &meshReader{r: bufio.NewReaderSize(c, 4096)}
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	stopBeat := make(chan struct{})
	defer close(stopBeat)
	for {
		f, err := r.next()
		if err != nil {
			if ctx.Err() == nil && !m.isReady() && isTimeout(err) {
				return errors.New("the radio didn't answer: is it a Meshtastic radio, with its network API on (port 4403)?")
			}
			return err
		}
		done, err := m.fromRadio(f, configID)
		if err != nil {
			return err
		}
		if done {
			_ = c.SetReadDeadline(time.Time{})
			go heartbeat(write, stopBeat, c)
		}
	}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// heartbeat keeps the radio from dropping a quiet connection.
func heartbeat(write func([]byte) error, stop chan struct{}, c io.Closer) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := write(pb(nil).bytes(7, nil)); err != nil {
				c.Close()
				return
			}
		}
	}
}

func (m *Meshtastic) isReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

// fromRadio handles one FromRadio message. done reports that the
// configuration has just finished downloading.
func (m *Meshtastic) fromRadio(data []byte, configID uint32) (done bool, err error) {
	fields, err := pbFields(data)
	if err != nil {
		return false, nil // not one we can read; the next frame may be fine
	}
	for _, f := range fields {
		switch {
		case f.num == 2 && f.typ == wireBytes: // packet
			if p, err := parseMeshPacket(f.b); err == nil {
				m.packet(p)
			}
		case f.num == 3 && f.typ == wireBytes: // my_info
			for _, g := range mustFields(f.b) {
				if g.num == 1 && g.typ == wireVarint {
					m.mu.Lock()
					m.myNum = uint32(g.u)
					m.mu.Unlock()
				}
			}
		case f.num == 4 && f.typ == wireBytes: // node_info
			m.nodeInfo(f.b)
		case f.num == 5 && f.typ == wireBytes: // config: lora (6) -> hop_limit (8)
			for _, g := range mustFields(f.b) {
				if g.num != 6 || g.typ != wireBytes {
					continue
				}
				for _, h := range mustFields(g.b) {
					if h.num == 8 && h.typ == wireVarint {
						m.mu.Lock()
						m.hopLimit = uint32(h.u)
						m.mu.Unlock()
					}
				}
			}
		case f.num == 7 && f.typ == wireVarint: // config_complete_id
			if uint32(f.u) != configID {
				continue
			}
			m.mu.Lock()
			first := !m.ready
			m.ready, m.err, m.since = true, "", time.Now()
			self := m.nodes[m.myNum]
			name := NodeID(m.myNum)
			if self != nil && self.longName != "" {
				name = self.longName + " (" + name + ")"
			}
			known := len(m.nodes)
			m.mu.Unlock()
			if first {
				m.logf("Meshtastic: connected to %s, which knows %d radios", name, max(known-1, 0))
			}
			done = first
		case f.num == 8 && f.typ == wireVarint && f.u != 0: // rebooted
			return false, errors.New("the radio restarted")
		}
	}
	return done, nil
}

func (m *Meshtastic) node(num uint32) *meshNode {
	n := m.nodes[num]
	if n == nil {
		if len(m.nodes) >= maxMeshNodes {
			var oldest *meshNode
			for _, o := range m.nodes {
				if o.num != m.myNum && (oldest == nil || o.lastHeard.Before(oldest.lastHeard)) {
					oldest = o
				}
			}
			if oldest != nil {
				delete(m.nodes, oldest.num)
			}
		}
		n = &meshNode{num: num}
		m.nodes[num] = n
	}
	return n
}

func (m *Meshtastic) nodeInfo(b []byte) {
	var num uint32
	var user meshUser
	var snr float32
	var heard uint32
	hasUser := false
	for _, f := range mustFields(b) {
		switch {
		case f.num == 1 && f.typ == wireVarint:
			num = uint32(f.u)
		case f.num == 2 && f.typ == wireBytes:
			user, hasUser = parseUser(f.b), true
		case f.num == 4 && f.typ == wireFixed32:
			snr = math.Float32frombits(uint32(f.u))
		case f.num == 5 && f.typ == wireFixed32:
			heard = uint32(f.u)
		}
	}
	if num == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.node(num)
	if hasUser {
		n.longName, n.shortName, n.hasKey = user.longName, user.shortName, user.hasKey
	}
	n.snr = snr
	if heard > 0 {
		n.lastHeard = time.Unix(int64(heard), 0)
	}
}

type meshUser struct {
	longName, shortName string
	hasKey              bool
}

func parseUser(b []byte) meshUser {
	var u meshUser
	for _, f := range mustFields(b) {
		if f.typ != wireBytes {
			continue
		}
		switch f.num {
		case 2:
			u.longName = cleanName(string(f.b))
		case 3:
			u.shortName = cleanName(string(f.b))
		case 8:
			u.hasKey = len(f.b) == 32
		}
	}
	return u
}

func cleanName(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
}

// packet handles a packet the radio received (or reports on).
func (m *Meshtastic) packet(p meshPacket) {
	m.mu.Lock()
	my := m.myNum
	if p.From != 0 && p.From != my {
		n := m.node(p.From)
		n.lastHeard = time.Now()
		if p.direct() {
			n.snr, n.direct = p.RxSNR, true
		} else if p.HopStart > 0 {
			n.direct = false
		}
	}
	m.mu.Unlock()
	if !p.Decoded {
		return // on a channel the home radio has no key for
	}
	switch p.Port {
	case portRouting:
		m.routing(p)
	case portNodeInfo:
		u := parseUser(p.Payload)
		m.mu.Lock()
		n := m.node(p.From)
		n.longName, n.shortName, n.hasKey = u.longName, u.shortName, u.hasKey || n.hasKey
		m.mu.Unlock()
	case portText:
		// Channel chat is for people; Sphynx only reads messages sent to
		// the home radio itself.
		if p.To != my || p.From == my || p.From == 0 {
			return
		}
		if m.repeat(p) {
			return
		}
		text := strings.TrimSpace(strings.ToValidUTF8(string(p.Payload), "?"))
		if text == "" {
			return
		}
		if p.Emoji != 0 {
			text = "reacted " + text
		}
		msg := Message{Link: "meshtastic", From: NodeID(p.From), Text: text, At: time.Now(), Via: "radio", Private: p.PKI}
		if p.ViaMQTT {
			msg.Via = "MQTT"
		}
		if p.HopStart > 0 && p.HopStart >= p.HopLimit {
			msg.Hops = int(p.HopStart - p.HopLimit)
		}
		if p.direct() {
			msg.SNR, msg.RSSI = p.RxSNR, p.RxRSSI
		}
		m.mu.Lock()
		if n := m.nodes[p.From]; n != nil {
			msg.FromName = n.longName
		}
		m.mu.Unlock()
		if !m.inbox.put(msg) {
			m.logf("Meshtastic: dropped a message from %s, too many waiting", msg.From)
		}
	}
}

// repeat reports whether this packet was seen in the last 10 minutes.
func (m *Meshtastic) repeat(p meshPacket) bool {
	key := uint64(p.From)<<32 | uint64(p.ID)
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, t := range m.seen {
		if time.Since(t) > 10*time.Minute {
			delete(m.seen, k)
		}
	}
	if _, ok := m.seen[key]; ok && p.ID != 0 {
		return true
	}
	m.seen[key] = time.Now()
	return false
}

// routing reads an acknowledgement (or a failure) for a sent message.
func (m *Meshtastic) routing(p meshPacket) {
	if p.RequestID == 0 {
		return
	}
	var reason uint64
	for _, f := range mustFields(p.Payload) {
		if f.num == 3 && f.typ == wireVarint {
			reason = f.u
		}
	}
	m.mu.Lock()
	pend := m.pending[p.RequestID]
	var r *Result
	switch {
	case pend == nil:
	case reason != 0:
		r = &Result{Err: meshError(reason)}
	case p.From == pend.to:
		r = &Result{Delivered: true}
	}
	// Anything else is the home radio (or a relay) hearing the message go
	// out: keep waiting for the other radio's own acknowledgement.
	if r != nil {
		delete(m.pending, p.RequestID)
	}
	m.mu.Unlock()
	if r != nil {
		pend.ch <- *r
	}
}

func meshError(reason uint64) error {
	msg, ok := map[uint64]string{
		1:  "no route to that radio",
		2:  "the other radio refused it",
		3:  "it timed out",
		4:  "the home radio has no way to send it",
		5:  "no answer after several tries: the other radio may be out of range or switched off",
		6:  "the other radio isn't on the same channel",
		7:  "the message is too long",
		8:  "no response",
		9:  "the home radio has used up the airtime it's allowed for now (duty cycle limit); try again later",
		32: "the other radio couldn't read it",
		33: "not authorized",
		34: "the radios couldn't encrypt it",
		35: "the other radio doesn't have the home radio's key yet",
		38: "too many messages at once; wait a little",
		39: "the home radio doesn't have the other radio's key yet",
	}[reason]
	if !ok {
		msg = fmt.Sprintf("the radio reported error %d", reason)
	}
	return errors.New(msg)
}

// Send sends a direct message (up to MaxMeshtasticText bytes) to a node
// and waits until the other radio acknowledges it or it fails.
func (m *Meshtastic) Send(ctx context.Context, to, text string) Result {
	num, ok := ParseNodeID(to)
	if !ok {
		return Result{Err: fmt.Errorf("%q isn't a Meshtastic node id (like !a1b2c3d4)", to)}
	}
	text = CutText(strings.TrimSpace(strings.ToValidUTF8(text, "?")), MaxMeshtasticText)
	if text == "" {
		return Result{Err: errors.New("nothing to send")}
	}
	m.mu.Lock()
	if !m.ready || m.write == nil {
		m.mu.Unlock()
		return Result{Err: errors.New("the home radio isn't connected")}
	}
	var id uint32
	for id == 0 || m.pending[id] != nil {
		id = rand.Uint32()
	}
	pend := &meshPending{to: num, ch: make(chan Result, 1)}
	m.pending[id] = pend
	write, hops := m.write, m.hopLimit
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}()
	if hops == 0 {
		hops = 3
	}
	data := pb(nil).varint(1, portText).bytes(2, []byte(text))
	pkt := pb(nil).fixed32(2, num).bytes(4, data).fixed32(6, id).varint(9, uint64(hops)).varint(10, 1)
	if err := write(pb(nil).bytes(1, pkt)); err != nil {
		return Result{Err: fmt.Errorf("couldn't hand it to the home radio: %w", err)}
	}
	wait := m.AckTimeout
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-pend.ch:
		return r
	case <-ctx.Done():
		return Result{Err: ctx.Err()}
	case <-t.C:
		return Result{Err: fmt.Errorf("no acknowledgement from %s", NodeID(num))}
	}
}

// Status reports the connection.
func (m *Meshtastic) Status() []LinkStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := LinkStatus{Name: "Meshtastic radio", Connected: m.ready, Error: m.err, Since: m.since}
	if m.ready {
		s.Detail = "home radio " + NodeID(m.myNum)
		if n := m.nodes[m.myNum]; n != nil && n.longName != "" {
			s.Detail = "home radio " + n.longName + " (" + NodeID(m.myNum) + ")"
		}
	} else if m.write != nil {
		s.Detail = "connecting"
	}
	return []LinkStatus{s}
}

// Nodes lists the radios the home radio knows, most recently heard first.
func (m *Meshtastic) Nodes() []Node {
	m.mu.Lock()
	out := make([]Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		if n.num == m.myNum {
			continue
		}
		out = append(out, Node{ID: NodeID(n.num), LongName: n.longName, ShortName: n.shortName,
			LastHeard: n.lastHeard, SNR: n.snr, Direct: n.direct, Key: n.hasKey})
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b Node) int {
		if c := b.LastHeard.Compare(a.LastHeard); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// CutText shortens text to at most n bytes without splitting a character.
func CutText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// ---- frames ----

// meshReader returns the frames in a stream from the radio, skipping
// anything else (a radio on USB also prints its log there).
type meshReader struct{ r *bufio.Reader }

func (m *meshReader) next() ([]byte, error) {
	for {
		b, err := m.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != meshStart1 {
			continue
		}
		if b, err = m.r.ReadByte(); err != nil {
			return nil, err
		}
		if b != meshStart2 {
			if b == meshStart1 {
				_ = m.r.UnreadByte()
			}
			continue
		}
		var hdr [2]byte
		if _, err := io.ReadFull(m.r, hdr[:]); err != nil {
			return nil, err
		}
		n := int(binary.BigEndian.Uint16(hdr[:]))
		if n > meshMaxFrame {
			continue // not really a frame start
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(m.r, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
}

// ---- protobuf ----

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// pb is a protobuf message being written.
type pb []byte

func (b pb) varint(field int, v uint64) pb {
	b = binary.AppendUvarint(b, uint64(field)<<3|wireVarint)
	return binary.AppendUvarint(b, v)
}

func (b pb) fixed32(field int, v uint32) pb {
	b = binary.AppendUvarint(b, uint64(field)<<3|wireFixed32)
	return binary.LittleEndian.AppendUint32(b, v)
}

func (b pb) bytes(field int, v []byte) pb {
	b = binary.AppendUvarint(b, uint64(field)<<3|wireBytes)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

// pbField is one field of a protobuf message.
type pbField struct {
	num int
	typ int
	u   uint64 // varint and fixed values
	b   []byte // length-delimited values
}

var errBadProto = errors.New("malformed protobuf")

func pbFields(data []byte) ([]pbField, error) {
	var out []pbField
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 || key>>3 > math.MaxInt32 {
			return nil, errBadProto
		}
		data = data[n:]
		f := pbField{num: int(key >> 3), typ: int(key & 7)}
		switch f.typ {
		case wireVarint:
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return nil, errBadProto
			}
			f.u, data = v, data[n:]
		case wireFixed64:
			if len(data) < 8 {
				return nil, errBadProto
			}
			f.u, data = binary.LittleEndian.Uint64(data), data[8:]
		case wireBytes:
			l, n := binary.Uvarint(data)
			if n <= 0 || l > uint64(len(data)-n) {
				return nil, errBadProto
			}
			f.b, data = data[n:n+int(l)], data[n+int(l):]
		case wireFixed32:
			if len(data) < 4 {
				return nil, errBadProto
			}
			f.u, data = uint64(binary.LittleEndian.Uint32(data)), data[4:]
		default:
			return nil, errBadProto
		}
		out = append(out, f)
	}
	return out, nil
}

// mustFields reads a nested message, or nothing if it is malformed.
func mustFields(data []byte) []pbField {
	f, _ := pbFields(data)
	return f
}

// meshPacket holds the fields of a MeshPacket (and its Data) Sphynx uses.
type meshPacket struct {
	From, To, ID       uint32
	Channel            uint32
	RxSNR              float32
	RxRSSI             int32
	HopLimit, HopStart uint32
	WantAck            bool
	ViaMQTT            bool
	PKI                bool

	Decoded   bool
	Port      uint32
	Payload   []byte
	RequestID uint32
	Emoji     uint32
}

// direct: heard straight from the sender, not through relays.
func (p meshPacket) direct() bool {
	return p.HopStart > 0 && p.HopStart == p.HopLimit && !p.ViaMQTT
}

func parseMeshPacket(b []byte) (meshPacket, error) {
	var p meshPacket
	fields, err := pbFields(b)
	if err != nil {
		return p, err
	}
	for _, f := range fields {
		switch {
		case f.num == 1 && f.typ == wireFixed32:
			p.From = uint32(f.u)
		case f.num == 2 && f.typ == wireFixed32:
			p.To = uint32(f.u)
		case f.num == 3 && f.typ == wireVarint:
			p.Channel = uint32(f.u)
		case f.num == 4 && f.typ == wireBytes:
			p.Decoded = true
			for _, d := range mustFields(f.b) {
				switch {
				case d.num == 1 && d.typ == wireVarint:
					p.Port = uint32(d.u)
				case d.num == 2 && d.typ == wireBytes:
					p.Payload = d.b
				case d.num == 6 && d.typ == wireFixed32:
					p.RequestID = uint32(d.u)
				case d.num == 8 && d.typ == wireFixed32:
					p.Emoji = uint32(d.u)
				}
			}
		case f.num == 6 && f.typ == wireFixed32:
			p.ID = uint32(f.u)
		case f.num == 8 && f.typ == wireFixed32:
			p.RxSNR = math.Float32frombits(uint32(f.u))
		case f.num == 9 && f.typ == wireVarint:
			p.HopLimit = uint32(f.u)
		case f.num == 10 && f.typ == wireVarint:
			p.WantAck = f.u != 0
		case f.num == 12 && f.typ == wireVarint:
			p.RxRSSI = int32(f.u) // negative values arrive sign-extended
		case f.num == 14 && f.typ == wireVarint:
			p.ViaMQTT = f.u != 0
		case f.num == 15 && f.typ == wireVarint:
			p.HopStart = uint32(f.u)
		case f.num == 17 && f.typ == wireVarint:
			p.PKI = f.u != 0
		}
	}
	return p, nil
}
