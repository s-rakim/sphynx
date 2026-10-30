// Package radiotest has pretend radios for trying and testing Sphynx's
// radio mode without hardware: a Meshtastic radio on the network, an
// APRS-IS server and a KISS TNC.
package radiotest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- Meshtastic ----

// MeshNode is another radio on the pretend mesh.
type MeshNode struct {
	Num       uint32
	LongName  string
	ShortName string
	// Ack is how the node answers direct messages: "ack" (the default),
	// "implicit" (the home radio first hears it relayed, then the real
	// acknowledgement comes), "none" (out of range: the home radio gives
	// up with MAX_RETRANSMIT) or "silent" (no answer at all).
	Ack string
}

// MeshSent is a direct message Sphynx sent through the pretend radio.
type MeshSent struct {
	To   uint32
	Text string
	At   time.Time
}

// Meshtastic is a pretend Meshtastic radio that Sphynx connects to over TCP.
type Meshtastic struct {
	MyNum    uint32
	MyName   string
	HopLimit uint32

	ln    net.Listener
	mu    sync.Mutex
	nodes map[uint32]*MeshNode
	conns map[net.Conn]bool
	sent  []MeshSent
}

// NewMeshtastic starts a pretend radio on addr ("127.0.0.1:0" for any port).
func NewMeshtastic(addr string, myNum uint32, myName string) (*Meshtastic, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	m := &Meshtastic{MyNum: myNum, MyName: myName, HopLimit: 3, ln: ln, nodes: map[uint32]*MeshNode{}, conns: map[net.Conn]bool{}}
	go m.accept()
	return m, nil
}

// Addr is where the pretend radio listens.
func (m *Meshtastic) Addr() string { return m.ln.Addr().String() }

// Close stops the radio and drops its connections.
func (m *Meshtastic) Close() {
	m.ln.Close()
	m.Drop()
}

// Drop disconnects Sphynx, like a radio restarting.
func (m *Meshtastic) Drop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for c := range m.conns {
		c.Close()
	}
}

// AddNode adds (or replaces) a radio the home radio knows.
func (m *Meshtastic) AddNode(n MeshNode) {
	m.mu.Lock()
	m.nodes[n.Num] = &n
	m.mu.Unlock()
}

// Connected reports whether Sphynx is connected.
func (m *Meshtastic) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.conns) > 0
}

// Sent lists what Sphynx sent.
func (m *Meshtastic) Sent() []MeshSent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MeshSent(nil), m.sent...)
}

// MeshIn describes a message for Inject.
type MeshIn struct {
	From      uint32
	Text      string
	Broadcast bool // to everyone on the channel instead of the home radio
	Hops      int  // relays it took
	Private   bool // a PKI-encrypted direct message
	SNR       float32
}

// Inject delivers a message from another radio to Sphynx.
func (m *Meshtastic) Inject(in MeshIn) error {
	to := m.MyNum
	if in.Broadcast {
		to = 0xFFFFFFFF
	}
	data := pb(nil).varint(1, 1).bytes(2, []byte(in.Text))
	start, rssi := uint64(3), int64(-80)
	pkt := pb(nil).fixed32(1, in.From).fixed32(2, to).bytes(4, data).fixed32(6, rand.Uint32()).
		fixed32(7, uint32(time.Now().Unix())).fixed32(8, math.Float32bits(in.SNR)).
		varint(9, start-uint64(in.Hops)).varint(12, uint64(rssi)).varint(15, start)
	if in.Private {
		pkt = pkt.varint(17, 1)
	}
	return m.toAll(pb(nil).bytes(2, pkt))
}

func (m *Meshtastic) toAll(msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.conns) == 0 {
		return errors.New("Sphynx isn't connected to the pretend radio")
	}
	for c := range m.conns {
		_ = writeMeshFrame(c, msg)
	}
	return nil
}

func (m *Meshtastic) accept() {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.serve(c)
	}
}

func (m *Meshtastic) serve(c net.Conn) {
	m.mu.Lock()
	m.conns[c] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.conns, c)
		m.mu.Unlock()
		c.Close()
	}()
	var wmu sync.Mutex
	send := func(msg []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = writeMeshFrame(c, msg)
	}
	r := bufio.NewReader(c)
	for {
		f, err := readMeshFrame(r)
		if err != nil {
			return
		}
		for _, fld := range fields(f) {
			switch {
			case fld.num == 3 && fld.typ == 0: // want_config_id
				m.sendConfig(send, uint32(fld.u))
			case fld.num == 4: // disconnect
				return
			case fld.num == 1 && fld.typ == 2: // packet
				m.received(send, fld.b)
			}
		}
	}
}

func (m *Meshtastic) sendConfig(send func([]byte), id uint32) {
	m.mu.Lock()
	nodes := []*MeshNode{{Num: m.MyNum, LongName: m.MyName, ShortName: "HOME"}}
	for _, n := range m.nodes {
		nodes = append(nodes, n)
	}
	hop := m.HopLimit
	m.mu.Unlock()
	send(pb(nil).bytes(3, pb(nil).varint(1, uint64(m.MyNum))))
	for _, n := range nodes {
		user := pb(nil).bytes(1, []byte(fmt.Sprintf("!%08x", n.Num))).bytes(2, []byte(n.LongName)).
			bytes(3, []byte(n.ShortName)).bytes(8, make([]byte, 32))
		info := pb(nil).varint(1, uint64(n.Num)).bytes(2, user).fixed32(4, math.Float32bits(6.5)).
			fixed32(5, uint32(time.Now().Add(-time.Minute).Unix()))
		send(pb(nil).bytes(4, info))
	}
	send(pb(nil).bytes(5, pb(nil).bytes(6, pb(nil).varint(8, uint64(hop)))))
	send(pb(nil).varint(7, uint64(id)))
}

// received handles a packet Sphynx asked the radio to send.
func (m *Meshtastic) received(send func([]byte), b []byte) {
	var to, id uint32
	var port uint64
	var payload []byte
	for _, f := range fields(b) {
		switch {
		case f.num == 2 && f.typ == 5:
			to = uint32(f.u)
		case f.num == 6 && f.typ == 5:
			id = uint32(f.u)
		case f.num == 4 && f.typ == 2:
			for _, d := range fields(f.b) {
				switch {
				case d.num == 1 && d.typ == 0:
					port = d.u
				case d.num == 2 && d.typ == 2:
					payload = d.b
				}
			}
		}
	}
	if port != 1 {
		return
	}
	m.mu.Lock()
	m.sent = append(m.sent, MeshSent{To: to, Text: string(payload), At: time.Now()})
	mode := "none"
	if n := m.nodes[to]; n != nil {
		mode = n.Ack
		if mode == "" {
			mode = "ack"
		}
	}
	m.mu.Unlock()
	routing := func(from uint32, reason uint64) {
		data := pb(nil).varint(1, 5).bytes(2, pb(nil).varint(3, reason)).fixed32(6, id)
		pkt := pb(nil).fixed32(1, from).fixed32(2, m.MyNum).bytes(4, data).fixed32(6, rand.Uint32())
		send(pb(nil).bytes(2, pkt))
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		switch mode {
		case "ack":
			routing(to, 0)
		case "implicit":
			routing(m.MyNum, 0)
			time.Sleep(50 * time.Millisecond)
			routing(to, 0)
		case "none":
			routing(m.MyNum, 5) // MAX_RETRANSMIT
		}
	}()
}

func writeMeshFrame(w io.Writer, msg []byte) error {
	_, err := w.Write(append([]byte{0x94, 0xC3, byte(len(msg) >> 8), byte(len(msg))}, msg...))
	return err
}

func readMeshFrame(r *bufio.Reader) ([]byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0x94 {
			continue
		}
		if b, err = r.ReadByte(); err != nil {
			return nil, err
		}
		if b != 0xC3 {
			continue
		}
		var hdr [2]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, err
		}
		buf := make([]byte, binary.BigEndian.Uint16(hdr[:]))
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
}

type pb []byte

func (b pb) varint(field int, v uint64) pb {
	return binary.AppendUvarint(binary.AppendUvarint(b, uint64(field)<<3), v)
}

func (b pb) fixed32(field int, v uint32) pb {
	return binary.LittleEndian.AppendUint32(binary.AppendUvarint(b, uint64(field)<<3|5), v)
}

func (b pb) bytes(field int, v []byte) pb {
	b = binary.AppendUvarint(binary.AppendUvarint(b, uint64(field)<<3|2), uint64(len(v)))
	return append(b, v...)
}

type field struct {
	num, typ int
	u        uint64
	b        []byte
}

func fields(data []byte) []field {
	var out []field
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return out
		}
		data = data[n:]
		f := field{num: int(key >> 3), typ: int(key & 7)}
		switch f.typ {
		case 0:
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return out
			}
			f.u, data = v, data[n:]
		case 2:
			l, n := binary.Uvarint(data)
			if n <= 0 || l > uint64(len(data)-n) {
				return out
			}
			f.b, data = data[n:n+int(l)], data[n+int(l):]
		case 5:
			if len(data) < 4 {
				return out
			}
			f.u, data = uint64(binary.LittleEndian.Uint32(data)), data[4:]
		default:
			return out
		}
		out = append(out, f)
	}
	return out
}

// ---- APRS-IS ----

// APRSIS is a pretend APRS-IS server.
type APRSIS struct {
	// Passcode is the one login accepted as verified.
	Passcode string

	ln    net.Listener
	mu    sync.Mutex
	conns map[net.Conn]string // connection -> logged in callsign
	lines []string
	// acking: stations that acknowledge messages sent to them.
	acking map[string]bool
}

// NewAPRSIS starts a pretend APRS-IS server on addr.
func NewAPRSIS(addr, passcode string) (*APRSIS, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &APRSIS{Passcode: passcode, ln: ln, conns: map[net.Conn]string{}, acking: map[string]bool{}}
	go s.accept()
	return s, nil
}

func (s *APRSIS) Addr() string { return s.ln.Addr().String() }

func (s *APRSIS) Close() {
	s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
}

// Acking makes a station acknowledge every message Sphynx sends it.
func (s *APRSIS) Acking(call string, on bool) {
	s.mu.Lock()
	s.acking[strings.ToUpper(call)] = on
	s.mu.Unlock()
}

// Lines lists the packets Sphynx sent, in APRS-IS text form.
func (s *APRSIS) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// LoggedIn returns the callsign Sphynx logged in with, if it is connected.
func (s *APRSIS) LoggedIn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.conns {
		if call != "" {
			return call
		}
	}
	return ""
}

// Inject sends Sphynx (logged in as to) a message from another station.
// An empty id sends it without asking for an acknowledgement.
func (s *APRSIS) Inject(from, to, text, id string) error {
	info := fmt.Sprintf(":%-9s:%s", to, text)
	if id != "" {
		info += "{" + id
	}
	return s.send(from + ">APRS,TCPIP*,qAC,FAKE:" + info)
}

func (s *APRSIS) send(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c, call := range s.conns {
		if call != "" {
			_, _ = c.Write([]byte(line + "\r\n"))
			n++
		}
	}
	if n == 0 {
		return errors.New("Sphynx isn't connected to the pretend APRS-IS server")
	}
	return nil
}

func (s *APRSIS) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c)
	}
}

func (s *APRSIS) serve(c net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
	}()
	s.mu.Lock()
	s.conns[c] = ""
	s.mu.Unlock()
	_, _ = c.Write([]byte("# fake aprsc 2.1\r\n"))
	r := bufio.NewReader(c)
	login, err := r.ReadString('\n')
	if err != nil {
		return
	}
	f := strings.Fields(login)
	if len(f) < 4 || f[0] != "user" || f[2] != "pass" {
		return
	}
	call := strings.ToUpper(f[1])
	verified := "unverified"
	if f[3] == s.Passcode {
		verified = "verified"
	}
	_, _ = c.Write([]byte(fmt.Sprintf("# logresp %s %s, server FAKE\r\n", call, verified)))
	s.mu.Lock()
	s.conns[c] = call
	s.mu.Unlock()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "#") {
			continue
		}
		s.mu.Lock()
		s.lines = append(s.lines, line)
		s.mu.Unlock()
		s.maybeAck(call, line)
	}
}

// maybeAck answers a message to an acking station.
func (s *APRSIS) maybeAck(from, line string) {
	_, info, ok := strings.Cut(line, ":")
	if !ok || len(info) < 11 || info[0] != ':' || info[10] != ':' {
		return
	}
	to := strings.TrimSpace(info[1:10])
	body := info[11:]
	i := strings.LastIndexByte(body, '{')
	if i < 0 || strings.HasPrefix(body, "ack") {
		return
	}
	s.mu.Lock()
	on := s.acking[strings.ToUpper(to)]
	s.mu.Unlock()
	if on {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = s.send(fmt.Sprintf("%s>APRS,TCPIP*,qAC,FAKE::%-9s:ack%s", to, from, body[i+1:]))
		}()
	}
}

// ---- KISS TNC ----

// KISS is a pretend KISS TNC (like Dire Wolf's KISS port).
type KISS struct {
	ln     net.Listener
	mu     sync.Mutex
	conns  map[net.Conn]bool
	frames [][]byte
}

func NewKISS(addr string) (*KISS, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	k := &KISS{ln: ln, conns: map[net.Conn]bool{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go k.serve(c)
		}
	}()
	return k, nil
}

func (k *KISS) Addr() string { return k.ln.Addr().String() }

func (k *KISS) Close() {
	k.ln.Close()
	k.mu.Lock()
	for c := range k.conns {
		c.Close()
	}
	k.mu.Unlock()
}

// Frames lists the AX.25 frames Sphynx sent, without KISS framing.
func (k *KISS) Frames() [][]byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([][]byte(nil), k.frames...)
}

// Connected reports whether Sphynx is connected.
func (k *KISS) Connected() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.conns) > 0
}

// Inject passes an AX.25 frame to Sphynx as if the radio heard it.
func (k *KISS) Inject(ax25 []byte) error {
	out := []byte{0xC0, 0x00}
	for _, b := range ax25 {
		switch b {
		case 0xC0:
			out = append(out, 0xDB, 0xDC)
		case 0xDB:
			out = append(out, 0xDB, 0xDD)
		default:
			out = append(out, b)
		}
	}
	out = append(out, 0xC0)
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.conns) == 0 {
		return errors.New("Sphynx isn't connected to the pretend TNC")
	}
	for c := range k.conns {
		_, _ = c.Write(out)
	}
	return nil
}

func (k *KISS) serve(c net.Conn) {
	k.mu.Lock()
	k.conns[c] = true
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		delete(k.conns, c)
		k.mu.Unlock()
		c.Close()
	}()
	r := bufio.NewReader(c)
	var frame []byte
	in, esc := false, false
	for {
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		switch {
		case b == 0xC0:
			if in && len(frame) > 1 && frame[0] == 0x00 {
				k.mu.Lock()
				k.frames = append(k.frames, append([]byte(nil), frame[1:]...))
				k.mu.Unlock()
			}
			in, frame = true, frame[:0]
		case esc:
			esc = false
			if b == 0xDC {
				frame = append(frame, 0xC0)
			} else {
				frame = append(frame, 0xDB)
			}
		case b == 0xDB:
			esc = true
		default:
			frame = append(frame, b)
		}
	}
}

// AX25 builds an AX.25 UI frame, as a radio would hear it. A "*" after a
// path entry marks a digipeater that has repeated it.
func AX25(src, dst string, path []string, info string) []byte {
	var out []byte
	addrs := append([]string{dst, src}, path...)
	for i, a := range addrs {
		repeated := strings.HasSuffix(a, "*")
		call, ssid, _ := strings.Cut(strings.TrimSuffix(a, "*"), "-")
		n, _ := strconv.Atoi(ssid)
		padded := fmt.Sprintf("%-6s", call)
		for j := 0; j < 6; j++ {
			out = append(out, padded[j]<<1)
		}
		b := byte(0x60 | n<<1)
		if i == 0 || i >= 2 && repeated {
			b |= 0x80
		}
		if i == len(addrs)-1 {
			b |= 0x01
		}
		out = append(out, b)
	}
	return append(append(out, 0x03, 0xF0), info...)
}

// DecodeAX25 writes a UI frame in APRS-IS text form, or "" if it isn't one.
func DecodeAX25(f []byte) string {
	var addrs []string
	i := 0
	for {
		if len(f) < i+7 {
			return ""
		}
		var call strings.Builder
		for j := 0; j < 6; j++ {
			if c := f[i+j] >> 1; c != ' ' {
				call.WriteByte(c)
			}
		}
		b := f[i+6]
		if ssid := (b >> 1) & 0x0F; ssid != 0 {
			call.WriteString("-" + strconv.Itoa(int(ssid)))
		}
		if len(addrs) >= 2 && b&0x80 != 0 {
			call.WriteByte('*')
		}
		addrs = append(addrs, call.String())
		i += 7
		if b&0x01 != 0 {
			break
		}
	}
	if len(addrs) < 2 || len(f) < i+2 || f[i] != 0x03 || f[i+1] != 0xF0 {
		return ""
	}
	s := addrs[1] + ">" + addrs[0]
	for _, p := range addrs[2:] {
		s += "," + p
	}
	return s + ":" + string(f[i+2:])
}
