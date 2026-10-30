package radio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// AX.25 UI frames and KISS framing, for APRS through a radio at home
// (a KISS TNC, or a sound-card modem such as Dire Wolf).

const (
	kissFEND  = 0xC0
	kissFESC  = 0xDB
	kissTFEND = 0xDC
	kissTFESC = 0xDD

	ax25UI     = 0x03
	ax25NoL3   = 0xF0
	maxAddress = 10 // destination, source and up to 8 digipeaters
)

// encodeAX25 turns a packet into an AX.25 UI frame.
func encodeAX25(p Packet) ([]byte, error) {
	if len(p.Path) > maxAddress-2 {
		return nil, errors.New("too many digipeaters")
	}
	var out []byte
	addrs := append([]string{p.Dst, p.Src}, p.Path...)
	for i, a := range addrs {
		repeated := strings.HasSuffix(a, "*")
		a = strings.TrimSuffix(a, "*")
		call, ssid, err := splitCall(a)
		if err != nil {
			return nil, fmt.Errorf("address %q: %w", a, err)
		}
		for j := 0; j < 6; j++ {
			c := byte(' ')
			if j < len(call) {
				c = call[j]
			}
			out = append(out, c<<1)
		}
		b := byte(0x60) | byte(ssid)<<1
		switch {
		case i == 0:
			b |= 0x80 // command frame: C bit set on the destination
		case i >= 2 && repeated:
			b |= 0x80 // H bit: this digipeater already repeated it
		}
		if i == len(addrs)-1 {
			b |= 0x01 // last address
		}
		out = append(out, b)
	}
	out = append(out, ax25UI, ax25NoL3)
	return append(out, p.Info...), nil
}

// decodeAX25 reads an AX.25 UI frame. The last digipeater that repeated
// the frame is marked with "*", as in APRS-IS text.
func decodeAX25(f []byte) (Packet, error) {
	var addrs []string
	repeated := -1
	i := 0
	for {
		if len(f) < i+7 {
			return Packet{}, errors.New("short address field")
		}
		var call strings.Builder
		for j := 0; j < 6; j++ {
			c := f[i+j] >> 1
			if c != ' ' {
				call.WriteByte(c)
			}
		}
		b := f[i+6]
		s := call.String()
		if ssid := (b >> 1) & 0x0F; ssid != 0 {
			s += "-" + strconv.Itoa(int(ssid))
		}
		if len(addrs) >= 2 && b&0x80 != 0 {
			repeated = len(addrs)
		}
		addrs = append(addrs, s)
		i += 7
		if b&0x01 != 0 {
			break
		}
		if len(addrs) == maxAddress {
			return Packet{}, errors.New("too many addresses")
		}
	}
	if len(addrs) < 2 {
		return Packet{}, errors.New("no source address")
	}
	if len(f) < i+2 || f[i] != ax25UI || f[i+1] != ax25NoL3 {
		return Packet{}, errors.New("not an APRS (UI) frame")
	}
	p := Packet{Dst: addrs[0], Src: addrs[1], Path: addrs[2:], Info: string(f[i+2:])}
	if repeated >= 2 {
		p.Path[repeated-2] += "*"
	}
	return p, nil
}

func splitCall(a string) (string, int, error) {
	call, ssid, has := strings.Cut(strings.ToUpper(a), "-")
	if call == "" || len(call) > 6 {
		return "", 0, errors.New("must be 1 to 6 letters or digits")
	}
	for _, c := range call {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", 0, errors.New("must be letters or digits")
		}
	}
	n := 0
	if has {
		var err error
		if n, err = strconv.Atoi(ssid); err != nil || n < 0 || n > 15 {
			return "", 0, errors.New("SSID must be 0 to 15")
		}
	}
	return call, n, nil
}

// kissFrame wraps an AX.25 frame for the TNC (port 0, data).
func kissFrame(ax []byte) []byte {
	out := []byte{kissFEND, 0x00}
	for _, b := range ax {
		switch b {
		case kissFEND:
			out = append(out, kissFESC, kissTFEND)
		case kissFESC:
			out = append(out, kissFESC, kissTFESC)
		default:
			out = append(out, b)
		}
	}
	return append(out, kissFEND)
}

// kissReader returns data frames from a KISS byte stream.
type kissReader struct {
	r *bufio.Reader
}

func (k *kissReader) next() ([]byte, error) {
	for {
		var frame []byte
		inFrame, esc := false, false
		for {
			b, err := k.r.ReadByte()
			if err != nil {
				return nil, err
			}
			if b == kissFEND {
				if inFrame && len(frame) > 0 {
					break
				}
				inFrame, frame = true, frame[:0]
				continue
			}
			if !inFrame {
				continue
			}
			switch {
			case esc:
				esc = false
				switch b {
				case kissTFEND:
					frame = append(frame, kissFEND)
				case kissTFESC:
					frame = append(frame, kissFESC)
				default:
					frame = append(frame, b)
				}
			case b == kissFESC:
				esc = true
			default:
				frame = append(frame, b)
			}
			if len(frame) > 2048 {
				inFrame, frame = false, nil
			}
		}
		// The low nibble of the first byte is the command: 0 is data.
		if frame[0]&0x0F == 0 {
			return frame[1:], nil
		}
	}
}

func newKISSReader(r io.Reader) *kissReader { return &kissReader{r: bufio.NewReader(r)} }
