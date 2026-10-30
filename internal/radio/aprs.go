// Package radio connects Sphynx to long-range radio networks that carry
// short text messages when there is no internet where you are: Meshtastic
// (LoRa mesh radios) and APRS (amateur packet radio, through the APRS-IS
// internet gateways or a radio at home).
//
// The protocol code is written from the public specifications: APRS 1.0.1
// (messages and acknowledgements), AX.25 2.2 and KISS for radio frames,
// and the Meshtastic client API's framing and message numbers.
package radio

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Packet is an APRS packet in the text form APRS-IS uses ("TNC2"):
// SRC>DST,PATH1,PATH2:information.
type Packet struct {
	Src  string
	Dst  string
	Path []string
	Info string
}

// ParsePacket reads one APRS-IS line.
func ParsePacket(line string) (Packet, error) {
	line = strings.TrimRight(line, "\r\n")
	head, info, ok := strings.Cut(line, ":")
	if !ok {
		return Packet{}, errors.New("no information field")
	}
	src, rest, ok := strings.Cut(head, ">")
	if !ok || src == "" {
		return Packet{}, errors.New("no source")
	}
	parts := strings.Split(rest, ",")
	if parts[0] == "" {
		return Packet{}, errors.New("no destination")
	}
	return Packet{Src: src, Dst: parts[0], Path: parts[1:], Info: info}, nil
}

func (p Packet) String() string {
	var b strings.Builder
	b.WriteString(p.Src)
	b.WriteByte('>')
	b.WriteString(p.Dst)
	for _, d := range p.Path {
		b.WriteByte(',')
		b.WriteString(d)
	}
	b.WriteByte(':')
	b.WriteString(p.Info)
	return b.String()
}

// Inner unwraps a third-party packet (one an internet gateway passed on
// to radio, "}SRC>DST,PATH:info"), so messages relayed that way are read
// as coming from their real sender.
func (p Packet) Inner() Packet {
	for i := 0; i < 3 && strings.HasPrefix(p.Info, "}"); i++ {
		inner, err := ParsePacket(p.Info[1:])
		if err != nil {
			break
		}
		p = inner
	}
	return p
}

var callRe = regexp.MustCompile(`^[A-Z0-9]{1,6}(-([0-9]|1[0-5]))?$`)

// NormalizeCall upper-cases a callsign and drops a "-0" SSID. It returns
// "" when it isn't a callsign that fits in a radio frame (up to six
// letters and digits, optionally -1 to -15).
func NormalizeCall(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	c = strings.TrimSuffix(c, "-0")
	if !callRe.MatchString(c) {
		return ""
	}
	return c
}

// MaxAPRSText is the longest text one APRS message can carry.
const MaxAPRSText = 67

// message is the information field of an APRS message, an
// acknowledgement or a rejection.
type message struct {
	To   string
	Text string
	// ID numbers a message that wants an acknowledgement.
	ID string
	// AckFor is a "reply-ack": the id of an earlier message this one
	// acknowledges as well.
	AckFor string
	// Ack and Rej mark an acknowledgement or rejection of message ID.
	Ack, Rej bool
}

// parseMessage reads an APRS message from an information field.
func parseMessage(info string) (message, bool) {
	if len(info) < 11 || info[0] != ':' || info[10] != ':' {
		return message{}, false
	}
	m := message{To: strings.TrimSpace(info[1:10])}
	body := info[11:]
	for _, kind := range []string{"ack", "rej"} {
		if len(body) > 3 && strings.EqualFold(body[:3], kind) && isMsgID(trimID(body[3:])) {
			m.ID = trimID(body[3:])
			m.Ack, m.Rej = kind == "ack", kind == "rej"
			return m, true
		}
	}
	if i := strings.LastIndexByte(body, '{'); i >= 0 {
		id := body[i+1:]
		ackFor := ""
		if j := strings.IndexByte(id, '}'); j >= 0 {
			id, ackFor = id[:j], id[j+1:]
		}
		id = strings.TrimSpace(id)
		if isMsgID(id) {
			m.ID, m.AckFor, body = id, strings.TrimSpace(ackFor), body[:i]
		}
	}
	m.Text = body
	return m, true
}

// trimID drops a reply-ack suffix ("}AA") and stray spaces from a message
// id in an acknowledgement.
func trimID(s string) string {
	if i := strings.IndexByte(s, '}'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func isMsgID(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func addressee(to string) string { return fmt.Sprintf("%-9s", to) }

// formatMessage is the information field of a message to "to". An empty
// id sends it without asking for an acknowledgement.
func formatMessage(to, text, id string) string {
	s := ":" + addressee(to) + ":" + text
	if id != "" {
		s += "{" + id
	}
	return s
}

func formatAck(to, id string) string { return ":" + addressee(to) + ":ack" + id }

// CleanAPRSText makes text safe to send in an APRS message: printable
// ASCII only, without the characters the format reserves.
func CleanAPRSText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '|' || r == '~' || r == '{':
			b.WriteByte(' ')
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		default:
			if a, ok := asciiFold[r]; ok {
				b.WriteString(a)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// asciiFold spells common non-ASCII characters in ASCII.
var asciiFold = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'é': "e", 'è': "e", 'ê': "e", 'ë': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ç': "c", 'ñ': "n", 'ß': "ss", 'Á': "A", 'É': "E", 'Í': "I",
	'Ó': "O", 'Ú': "U", 'Ä': "A", 'Ö': "O", 'Ü': "U", 'Ç': "C", 'Ñ': "N", '‘': "'", '’': "'", '“': `"`,
	'”': `"`, '–': "-", '—': "-", '…': "...", '°': " deg", '€': "EUR", '£': "GBP",
}

// SplitText cuts text into pieces of at most max bytes, at spaces where it
// can, for links that carry only short messages.
func SplitText(text string, max int) []string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		if text == "" {
			return nil
		}
		return []string{text}
	}
	var out []string
	for len(text) > max {
		cut := strings.LastIndexByte(text[:max+1], ' ')
		if cut < max/2 {
			cut = max
			// Don't split a UTF-8 sequence.
			for cut > 0 && text[cut]&0xC0 == 0x80 {
				cut--
			}
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}
