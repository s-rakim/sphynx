package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/s-rakim/sphynx/internal/chat"
	"github.com/s-rakim/sphynx/internal/radio"
)

// Radio mode: where there is no internet at all, a long-range radio can
// still reach Sphynx at home with short texts. A Meshtastic radio (LoRa)
// or an APRS station (amateur packet radio) connected to Sphynx receives
// them and:
//   - answers commands from your own radios: status, ping, and wake a
//     computer (with a one-time code from an authenticator app);
//   - passes everything else into Sphynx Chat, where each radio station
//     is a contact, and sends the replies back over the radio.
//
// It carries text, not internet traffic.

// RadioSettings are on the Radio page.
type RadioSettings struct {
	Enabled    bool               `json:"enabled"`
	Meshtastic MeshtasticSettings `json:"meshtastic"`
	APRS       APRSSettings       `json:"aprs"`
	Contacts   []RadioContact     `json:"contacts"`
	// DeliverTo are the devices (Headscale node ids) that get chat
	// messages from a radio contact no device is chatting with yet. None
	// means Sphynx home.
	DeliverTo []string `json:"deliverTo"`
}

type MeshtasticSettings struct {
	Enabled bool `json:"enabled"`
	// Address is the radio's IP address or name on the home network (it
	// needs Wi-Fi or Ethernet), or a USB serial port like /dev/ttyUSB0.
	Address string `json:"address"`
}

type APRSSettings struct {
	Enabled bool `json:"enabled"`
	// Call is Sphynx's own callsign with an SSID, like 5Z4ABC-10. APRS is
	// amateur radio, which needs a licence.
	Call string `json:"call"`
	// Server is the APRS-IS server, "" for none. The passcode is a secret.
	Server string `json:"server"`
	// KISS is the host:port of a KISS TNC on a radio at home, like Dire Wolf.
	KISS string `json:"kiss"`
	// Path is the digipeater path for packets sent through the TNC.
	Path []string `json:"path"`
}

// RadioContact is a radio station that may use Sphynx.
type RadioContact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Link    string `json:"link"`    // meshtastic or aprs
	Address string `json:"address"` // node id (!a1b2c3d4) or callsign
	// Commands: the station may use status, ping and wake.
	Commands bool `json:"commands"`
	// Chat: the station is a contact in Sphynx Chat.
	Chat bool `json:"chat"`
}

func defaultRadioSettings() RadioSettings {
	return RadioSettings{
		APRS:      APRSSettings{Server: "rotate.aprs2.net:14580", Path: []string{"WIDE1-1", "WIDE2-1"}},
		Contacts:  []RadioContact{},
		DeliverTo: []string{},
	}
}

// key is the contact's address in the form the chat and the links use.
func (c RadioContact) key() string { return c.Link + ":" + c.Address }

var hostPortRe = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9a-fA-F:.]+\](:[0-9]{1,5})?$`)

func validHostPort(s string) bool {
	if !hostPortRe.MatchString(s) {
		return false
	}
	if _, port, err := net.SplitHostPort(s); err == nil {
		var n int
		if _, err := fmt.Sscan(port, &n); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

// normalize tidies what the admin typed and checks it.
func (s *RadioSettings) normalize() error {
	s.Meshtastic.Address = strings.TrimSpace(s.Meshtastic.Address)
	if s.Meshtastic.Enabled {
		a := s.Meshtastic.Address
		if !(strings.HasPrefix(a, "/dev/") && !strings.Contains(a, "..")) && !validHostPort(a) {
			return errors.New("enter the Meshtastic radio's IP address on your network (like 192.168.1.50), or a USB port like /dev/ttyUSB0")
		}
	}
	p := &s.APRS
	p.Call = strings.ToUpper(strings.TrimSpace(p.Call))
	p.Server, p.KISS = strings.TrimSpace(p.Server), strings.TrimSpace(p.KISS)
	if p.Path == nil {
		p.Path = []string{}
	}
	for i, d := range p.Path {
		p.Path[i] = strings.ToUpper(strings.TrimSpace(d))
		if radio.NormalizeCall(p.Path[i]) == "" {
			return fmt.Errorf("%q isn't a digipeater path entry (like WIDE1-1)", d)
		}
	}
	if len(p.Path) > 8 {
		return errors.New("use at most 8 digipeaters")
	}
	if p.Enabled {
		if c := radio.NormalizeCall(p.Call); c == "" {
			return errors.New("enter your callsign with an SSID, like 5Z4ABC-10")
		} else {
			p.Call = c
		}
		if p.Server == "" && p.KISS == "" {
			return errors.New("APRS needs an APRS-IS server, a radio (KISS TNC) at home, or both")
		}
		if p.Server != "" && !validHostPort(p.Server) {
			return errors.New("the APRS-IS server must look like rotate.aprs2.net:14580")
		}
		if p.KISS != "" && !validHostPort(p.KISS) {
			return errors.New("the KISS TNC must look like 192.168.1.60:8001")
		}
	}
	if len(s.Contacts) > 100 {
		return errors.New("Sphynx keeps at most 100 radio contacts")
	}
	seen := map[string]bool{}
	for i := range s.Contacts {
		c := &s.Contacts[i]
		c.Name = strings.TrimSpace(c.Name)
		c.Address = strings.TrimSpace(c.Address)
		if c.Name == "" || len([]rune(c.Name)) > 60 {
			return errors.New("give each radio contact a name (up to 60 characters)")
		}
		switch c.Link {
		case "meshtastic":
			n, ok := radio.ParseNodeID(c.Address)
			if !ok {
				return fmt.Errorf("%s: a Meshtastic node id looks like !a1b2c3d4 (shown in the Meshtastic app)", c.Name)
			}
			c.Address = radio.NodeID(n)
		case "aprs":
			if c.Address = radio.NormalizeCall(c.Address); c.Address == "" {
				return fmt.Errorf("%s: enter a callsign, like 5Z4ABC-7", c.Name)
			}
		default:
			return fmt.Errorf("%s: pick Meshtastic or APRS", c.Name)
		}
		if seen[c.key()] {
			return fmt.Errorf("%s is listed twice", c.Address)
		}
		seen[c.key()] = true
		if c.ID == "" {
			c.ID = randomHex(6)
		}
	}
	if s.Contacts == nil {
		s.Contacts = []RadioContact{}
	}
	if s.DeliverTo == nil {
		s.DeliverTo = []string{}
	}
	if len(s.DeliverTo) > 50 {
		return errors.New("pick at most 50 devices")
	}
	return nil
}

// ---- the service ----

type radioService struct {
	a *App

	mu      sync.Mutex
	stop    context.CancelFunc
	done    chan struct{}
	mesh    *radio.Meshtastic
	aprs    *radio.APRS
	heard   []radioHeard // stations that aren't contacts
	log     []radioLogEntry
	sending map[string]*sync.Mutex // one message at a time per station

	codeMu   sync.Mutex
	lastStep int64       // last wake code used, never accepted again
	badCodes []time.Time // wrong wake codes, to stop guessing
}

type radioHeard struct {
	Link string    `json:"link"`
	From string    `json:"from"`
	Name string    `json:"name,omitempty"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type radioLogEntry struct {
	At      time.Time `json:"at"`
	Out     bool      `json:"out"`
	Link    string    `json:"link"`
	Station string    `json:"station"`
	Text    string    `json:"text"`
	Result  string    `json:"result,omitempty"`
}

func newRadioService(a *App) *radioService {
	return &radioService{a: a, sending: map[string]*sync.Mutex{}}
}

func (r *radioService) note(e radioLogEntry) {
	e.At = time.Now()
	r.mu.Lock()
	r.log = append(r.log, e)
	if len(r.log) > 200 {
		r.log = r.log[len(r.log)-200:]
	}
	r.mu.Unlock()
}

// apply (re)starts the radio links after the settings changed.
func (r *radioService) apply(ctx context.Context) {
	r.mu.Lock()
	stop, done := r.stop, r.done
	r.stop, r.done, r.mesh, r.aprs = nil, nil, nil, nil
	r.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	s := r.a.settings.Get().Radio
	r.syncChat()
	if !s.Enabled {
		return
	}
	lctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	var mesh *radio.Meshtastic
	var aprs *radio.APRS
	if s.Meshtastic.Enabled {
		mesh = &radio.Meshtastic{Address: s.Meshtastic.Address, OnMessage: r.received, Logf: r.a.Logf}
		wg.Go(func() { mesh.Run(lctx) })
	}
	if s.APRS.Enabled {
		aprs = &radio.APRS{Call: s.APRS.Call, Passcode: r.a.secrets.Get().APRSPasscode, IS: s.APRS.Server, KISS: s.APRS.KISS,
			Path: s.APRS.Path, Version: r.a.cfg.Version, OnMessage: r.received, Logf: r.a.Logf}
		wg.Go(func() { aprs.Run(lctx) })
	}
	done = make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	r.mu.Lock()
	r.stop, r.done, r.mesh, r.aprs = cancel, done, mesh, aprs
	r.mu.Unlock()
}

// syncChat shows the radio contacts that may chat in Sphynx Chat.
func (r *radioService) syncChat() {
	s := r.a.settings.Get()
	var contacts []chat.RadioContact
	if s.Radio.Enabled {
		for _, c := range s.Radio.Contacts {
			if !c.Chat || (c.Link == "meshtastic" && !s.Radio.Meshtastic.Enabled) || (c.Link == "aprs" && !s.Radio.APRS.Enabled) {
				continue
			}
			detail, maxText := "Meshtastic radio "+c.Address, meshChatText
			if c.Link == "aprs" {
				detail, maxText = "APRS station "+c.Address, aprsChatText
			}
			contacts = append(contacts, chat.RadioContact{Key: c.key(), Name: c.Name, Detail: detail, Link: c.Link, MaxText: maxText})
		}
	}
	r.a.chat.srv.SetRadio(contacts, r.chatSend)
}

// chatSend passes a chat message on to a radio contact.
func (r *radioService) chatSend(ctx context.Context, key, text string) error {
	link, addr, _ := strings.Cut(key, ":")
	name := addr
	for _, c := range r.a.settings.Get().Radio.Contacts {
		if c.key() == key {
			name = c.Name
		}
	}
	err := r.send(ctx, link, addr, text)
	e := radioLogEntry{Out: true, Link: link, Station: name, Text: "(chat message)", Result: "delivered"}
	if err != nil {
		e.Result = "not delivered: " + err.Error()
	}
	r.note(e)
	return err
}

// How long a chat message for a radio contact can be: what fits in the
// few radio messages send splits it into, with room for splitting between
// words.
const (
	meshChatText = 520 // of 3 messages of 200 bytes
	aprsChatText = 240 // of 4 messages of 67 characters
)

// send sends text to a station, split into as many messages as the link
// needs (at most a few), one after the other.
func (r *radioService) send(ctx context.Context, link, addr, text string) error {
	r.mu.Lock()
	mesh, aprs := r.mesh, r.aprs
	l := r.sending[link+":"+addr]
	if l == nil {
		l = &sync.Mutex{}
		r.sending[link+":"+addr] = l
	}
	r.mu.Unlock()
	l.Lock()
	defer l.Unlock()

	var parts []string
	var one func(string) radio.Result
	maxParts := 3
	switch link {
	case "meshtastic":
		if mesh == nil {
			return errors.New("Meshtastic is turned off")
		}
		parts = radio.SplitText(text, radio.MaxMeshtasticText)
		one = func(p string) radio.Result { return mesh.Send(ctx, addr, p) }
	case "aprs":
		if aprs == nil {
			return errors.New("APRS is turned off")
		}
		parts, maxParts = radio.SplitText(radio.CleanAPRSText(text), radio.MaxAPRSText), 4
		one = func(p string) radio.Result { return aprs.Send(ctx, addr, p) }
	default:
		return fmt.Errorf("unknown radio link %q", link)
	}
	if len(parts) == 0 {
		return errors.New("nothing to send")
	}
	if len(parts) > maxParts {
		parts = parts[:maxParts]
		last := parts[maxParts-1]
		parts[maxParts-1] = radio.CutText(last, len(last)-3) + "..."
	}
	for i, p := range parts {
		if res := one(p); res.Err != nil {
			if len(parts) > 1 {
				return fmt.Errorf("part %d of %d: %w", i+1, len(parts), res.Err)
			}
			return res.Err
		}
	}
	return nil
}

// reply answers a station in the background.
func (r *radioService) reply(c RadioContact, text string) {
	go func() {
		ctx, cancel := context.WithTimeout(r.a.ctx, 15*time.Minute)
		defer cancel()
		err := r.send(ctx, c.Link, c.Address, text)
		e := radioLogEntry{Out: true, Link: c.Link, Station: c.Name, Text: text, Result: "delivered"}
		if err != nil {
			e.Result = "not delivered: " + err.Error()
		}
		r.note(e)
	}()
}

// contact finds who sent a message.
func (r *radioService) contact(m radio.Message) (RadioContact, bool) {
	for _, c := range r.a.settings.Get().Radio.Contacts {
		if c.Link == m.Link && strings.EqualFold(c.Address, m.From) {
			return c, true
		}
	}
	return RadioContact{}, false
}

// received handles a message from a radio, on the link's own goroutine.
func (r *radioService) received(m radio.Message) {
	c, ok := r.contact(m)
	if !ok {
		r.mu.Lock()
		r.heard = slices.DeleteFunc(r.heard, func(h radioHeard) bool { return h.Link == m.Link && h.From == m.From })
		r.heard = append(r.heard, radioHeard{Link: m.Link, From: m.From, Name: m.FromName, Text: radio.CutText(m.Text, 80), At: m.At})
		if len(r.heard) > 20 {
			r.heard = r.heard[len(r.heard)-20:]
		}
		r.mu.Unlock()
		r.a.Logf("radio: ignored a message from %s, which isn't one of your radio contacts", m.From)
		return
	}
	ctx, cancel := context.WithTimeout(r.a.ctx, time.Minute)
	defer cancel()
	cmd, isCmd := radioCommand{}, false
	if c.Commands {
		cmd, isCmd = r.parseCommand(m.Text)
	}
	if isCmd {
		r.note(radioLogEntry{Link: m.Link, Station: c.Name, Text: cmd.logText()})
		r.reply(c, r.run(ctx, c, m, cmd))
		return
	}
	if c.Chat && r.a.settings.Get().Chat.Enabled {
		names, err := r.a.chat.srv.RadioReceived(c.key(), m.Text, r.deliverTo(ctx))
		e := radioLogEntry{Link: m.Link, Station: c.Name, Text: "(chat message)", Result: "passed to " + strings.Join(names, ", ")}
		if err != nil {
			e.Result = "not passed on: " + err.Error()
			r.reply(c, "Sphynx: not delivered, "+err.Error())
		}
		r.note(e)
		return
	}
	r.note(radioLogEntry{Link: m.Link, Station: c.Name, Text: radio.CutText(m.Text, 80), Result: "not a command"})
	if c.Commands {
		r.reply(c, "Sphynx: unknown command. Send help for the list.")
	}
}

// deliverTo is who gets chat messages nobody is chatting about yet.
func (r *radioService) deliverTo(ctx context.Context) []string {
	nodes := r.a.settings.Get().Radio.DeliverTo
	if len(nodes) == 0 {
		if home := r.a.helpers.homeNodeID(); home != "" {
			nodes = []string{home}
		}
	}
	var ids []string
	for _, n := range nodes {
		if id, ok := r.a.chat.dir.byNode(ctx, n); ok {
			ids = append(ids, id.ID)
		}
	}
	return ids
}

// ---- commands ----

type radioCommand struct {
	name string // help, ping, status, wake
	arg  string // device or computer
	code string
}

func (c radioCommand) logText() string {
	s := strings.TrimSpace(c.name + " " + c.arg)
	if c.code != "" {
		s += " ******"
	}
	return s
}

var codeRe = regexp.MustCompile(`^[0-9]{6}$`)

// parseCommand reads a command: the whole message, optionally starting
// with "/". "status" and "wake" only count when they name a device or
// computer Sphynx knows (or carry a code), so ordinary sentences that
// happen to start with those words still go to the chat.
func (r *radioService) parseCommand(text string) (radioCommand, bool) {
	t := strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(text), "/")), " ")
	word, rest, _ := strings.Cut(t, " ")
	switch strings.ToLower(word) {
	case "help", "?", "commands":
		if rest == "" {
			return radioCommand{name: "help"}, true
		}
	case "ping":
		if rest == "" {
			return radioCommand{name: "ping"}, true
		}
	case "status":
		if rest == "" {
			return radioCommand{name: "status"}, true
		}
		ctx, cancel := context.WithTimeout(r.a.ctx, 10*time.Second)
		defer cancel()
		devs, err := r.a.devices(ctx)
		if err == nil && findDevice(devs, rest) != nil {
			return radioCommand{name: "status", arg: rest}, true
		}
	case "wake":
		f := strings.Fields(rest)
		if len(f) >= 2 && codeRe.MatchString(f[len(f)-1]) {
			return radioCommand{name: "wake", arg: strings.Join(f[:len(f)-1], " "), code: f[len(f)-1]}, true
		}
		if rest != "" && r.wakeTarget(rest) != nil {
			return radioCommand{name: "wake", arg: rest}, true
		}
	}
	return radioCommand{}, false
}

func (r *radioService) run(ctx context.Context, c RadioContact, m radio.Message, cmd radioCommand) string {
	switch cmd.name {
	case "help":
		s := "Sphynx: status, status <device>, ping, wake <computer> <code>"
		if c.Chat {
			s += ". Anything else goes to Sphynx Chat; start with @device to reach one device."
		}
		return s
	case "ping":
		return "Sphynx: pong" + signal(m)
	case "status":
		if cmd.arg != "" {
			return r.deviceStatus(ctx, cmd.arg)
		}
		return r.status(ctx)
	case "wake":
		return r.wake(c, cmd)
	}
	return "Sphynx: unknown command"
}

// signal describes how a message reached Sphynx.
func signal(m radio.Message) string {
	switch {
	case m.Via == "MQTT":
		return " (reached the home radio through MQTT)"
	case m.Link == "meshtastic" && m.Hops == 0:
		return fmt.Sprintf(" (heard you directly: SNR %.1f dB, RSSI %d dBm)", m.SNR, m.RSSI)
	case m.Link == "meshtastic":
		return fmt.Sprintf(" (heard you through %d relays)", m.Hops)
	case m.Via != "":
		return " (via " + m.Via + ")"
	}
	return ""
}

// status is an overview of the network in a few words.
func (r *radioService) status(ctx context.Context) string {
	devs, err := r.a.devices(ctx)
	if err != nil {
		return "Sphynx is up, but can't list devices right now: " + err.Error()
	}
	var on, off []string
	for _, d := range devs {
		if d.Helper {
			continue
		}
		if d.Online {
			on = append(on, displayName(d.Node))
		} else {
			off = append(off, displayName(d.Node))
		}
	}
	s := fmt.Sprintf("Sphynx OK. Online %d/%d", len(on), len(on)+len(off))
	if len(on) > 0 {
		s += ": " + strings.Join(on, ", ")
	}
	if r.a.settings.Get().Home.ExitNode {
		s += ". Home exit node on"
	}
	return s + "."
}

func (r *radioService) deviceStatus(ctx context.Context, name string) string {
	devs, err := r.a.devices(ctx)
	if err != nil {
		return "Sphynx can't list devices right now: " + err.Error()
	}
	d := findDevice(devs, name)
	if d == nil {
		return "Sphynx: no device called " + name
	}
	if d.Online {
		return fmt.Sprintf("%s: online, %s", displayName(d.Node), firstIPv4(d.IPAddresses))
	}
	if d.LastSeen != nil && !d.LastSeen.IsZero() {
		return fmt.Sprintf("%s: offline, last seen %s ago", displayName(d.Node), roughDuration(time.Since(*d.LastSeen)))
	}
	return displayName(d.Node) + ": offline"
}

func firstIPv4(ips []string) string {
	for _, ip := range ips {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

func roughDuration(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return "1 minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// nameKey compares names the way people type them on a small keypad.
func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// findDevice finds a device by name: exactly, or the one whose name starts
// with what was typed.
func findDevice(devs []Device, name string) *Device {
	want := nameKey(name)
	if want == "" {
		return nil
	}
	var prefix []*Device
	for i := range devs {
		d := &devs[i]
		if d.Helper {
			continue
		}
		for _, n := range []string{displayName(d.Node), d.Name, d.Hostname} {
			k := nameKey(n)
			if k == want {
				return d
			}
			if k != "" && strings.HasPrefix(k, want) {
				prefix = append(prefix, d)
				break
			}
		}
	}
	if len(prefix) == 1 {
		return prefix[0]
	}
	return nil
}

func (r *radioService) wakeTarget(name string) *WakeTarget {
	want := nameKey(name)
	for _, t := range r.a.state.Get().WakeTargets {
		if nameKey(t.Name) == want {
			return t
		}
	}
	return nil
}

func (r *radioService) wake(c RadioContact, cmd radioCommand) string {
	t := r.wakeTarget(cmd.arg)
	if t == nil {
		var names []string
		for _, t := range r.a.state.Get().WakeTargets {
			names = append(names, t.Name)
		}
		if len(names) == 0 {
			return "Sphynx: no computers to wake yet; add them on the Wake-on-LAN page"
		}
		return "Sphynx: no computer called " + cmd.arg + ". Computers: " + strings.Join(names, ", ")
	}
	if cmd.code == "" {
		return "Sphynx: add the 6-digit code from your authenticator app: wake " + t.Name + " 123456"
	}
	if err := r.checkCode(cmd.code); err != nil {
		r.a.Logf("radio: %s tried to wake %s: %v", c.Name, t.Name, err)
		return "Sphynx: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(r.a.ctx, time.Minute)
	defer cancel()
	result, err := r.a.wakeAndRecord(ctx, t)
	if err != nil {
		return "Sphynx: couldn't wake " + t.Name + ": " + err.Error()
	}
	r.a.Logf("radio: %s woke %s", c.Name, t.Name)
	if t.NodeID != "" {
		go r.watchWake(c, t)
	}
	return "Sphynx: wake signal sent to " + t.Name + " (" + result + ")"
}

// watchWake tells the station when the woken computer is online.
func (r *radioService) watchWake(c RadioContact, t *WakeTarget) {
	ctx, cancel := context.WithTimeout(r.a.ctx, 5*time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			if r.a.ctx.Err() == nil {
				r.reply(c, "Sphynx: "+t.Name+" isn't online after 5 minutes")
			}
			return
		case <-time.After(10 * time.Second):
		}
		if n, err := r.a.hs.GetNode(ctx, t.NodeID); err == nil && n.Online {
			r.reply(c, "Sphynx: "+t.Name+" is online")
			return
		}
	}
}

// checkCode accepts a current wake code once. Five wrong codes in an hour
// lock waking until the hour is over.
func (r *radioService) checkCode(code string) error {
	secret := r.a.secrets.Get().RadioTOTP
	if secret == "" {
		return errors.New("wake codes aren't set up; set them up on the Radio page")
	}
	r.codeMu.Lock()
	defer r.codeMu.Unlock()
	r.badCodes = slices.DeleteFunc(r.badCodes, func(t time.Time) bool { return time.Since(t) > time.Hour })
	if len(r.badCodes) >= 5 {
		return errors.New("too many wrong codes; waking is locked for up to an hour")
	}
	step, ok := totpCheck(secret, code, time.Now())
	if !ok || step <= r.lastStep {
		r.badCodes = append(r.badCodes, time.Now())
		return errors.New("wrong or used code")
	}
	r.lastStep = step
	return nil
}

// ---- admin API ----

func (a *App) apiRadio(w http.ResponseWriter, r *http.Request) (any, error) {
	rs := a.radio
	rs.mu.Lock()
	var links []radio.LinkStatus
	var nodes []radio.Node
	if rs.mesh != nil {
		links = append(links, rs.mesh.Status()...)
		nodes = rs.mesh.Nodes()
	}
	if rs.aprs != nil {
		links = append(links, rs.aprs.Status()...)
	}
	heard := slices.Clone(rs.heard)
	log := slices.Clone(rs.log)
	rs.mu.Unlock()
	slices.Reverse(heard)
	slices.Reverse(log)
	if links == nil {
		links = []radio.LinkStatus{}
	}
	if nodes == nil {
		nodes = []radio.Node{}
	}
	var targets []string
	for _, t := range a.state.Get().WakeTargets {
		targets = append(targets, t.Name)
	}
	sec := a.secrets.Get()
	return map[string]any{
		"settings":    a.settings.Get().Radio,
		"passcodeSet": sec.APRSPasscode != "",
		"codeSet":     sec.RadioTOTP != "",
		"links":       links,
		"nodes":       nodes,
		"heard":       append([]radioHeard{}, heard...),
		"log":         append([]radioLogEntry{}, log...),
		"wakeTargets": append([]string{}, targets...),
		"chatOn":      a.settings.Get().Chat.Enabled,
	}, nil
}

func (a *App) apiRadioSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		RadioSettings
		// Passcode replaces the APRS-IS passcode when set; ClearPasscode
		// removes it.
		Passcode      string `json:"passcode"`
		ClearPasscode bool   `json:"clearPasscode"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	s := req.RadioSettings
	if err := s.normalize(); err != nil {
		return nil, badRequest(err.Error())
	}
	pass := strings.TrimSpace(req.Passcode)
	if pass != "" && !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(pass) {
		return nil, badRequest("an APRS-IS passcode is a number of up to 5 digits")
	}
	if pass != "" || req.ClearPasscode {
		if err := a.secrets.Update(func(sec *Secrets) error {
			sec.APRSPasscode = pass
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := a.settings.Update(func(cur *Settings) error {
		cur.Radio = s
		return nil
	}); err != nil {
		return nil, err
	}
	a.Logf("radio settings updated")
	a.radio.apply(a.ctx)
	return a.apiRadio(w, r)
}

// apiRadioCode makes a new wake code secret, shown once as a QR code for
// an authenticator app. The old one stops working.
func (a *App) apiRadioCode(w http.ResponseWriter, r *http.Request) (any, error) {
	secret := newTOTPSecret()
	if err := a.secrets.Update(func(sec *Secrets) error {
		sec.RadioTOTP = secret
		return nil
	}); err != nil {
		return nil, err
	}
	uri := totpURI(secret)
	qr, err := qrDataURL(uri)
	if err != nil {
		return nil, err
	}
	a.Logf("radio: new wake code secret created")
	return map[string]string{"secret": secret, "uri": uri, "qr": qr}, nil
}

func (a *App) apiRadioCodeDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	return nil, a.secrets.Update(func(sec *Secrets) error {
		sec.RadioTOTP = ""
		return nil
	})
}

// apiRadioTest sends a message to a contact; the log shows how it went.
func (a *App) apiRadioTest(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		ContactID string `json:"contactId"`
		Text      string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		text = "Sphynx: test message"
	}
	for _, c := range a.settings.Get().Radio.Contacts {
		if c.ID == req.ContactID {
			a.radio.reply(c, text)
			return nil, nil
		}
	}
	return nil, notFound("no such radio contact")
}
