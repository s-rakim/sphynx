// Package wol builds and sends Wake-on-LAN magic packets.
package wol

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// ParseMAC accepts aa:bb:cc:dd:ee:ff, aa-bb-..., or aabbccddeeff and returns
// the canonical lowercase colon form.
func ParseMAC(s string) (net.HardwareAddr, error) {
	s = strings.TrimSpace(s)
	if len(s) == 12 && !strings.ContainsAny(s, ":-.") {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(s[i : i+2])
		}
		s = b.String()
	}
	mac, err := net.ParseMAC(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not a MAC address (expected something like 3c:7c:3f:12:34:56)", s)
	}
	if len(mac) != 6 {
		return nil, errors.New("Wake-on-LAN needs a 6-byte Ethernet MAC address")
	}
	return mac, nil
}

// MagicPacket is 6 bytes of 0xff followed by the MAC repeated 16 times.
func MagicPacket(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		p = append(p, 0xff)
	}
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p
}

// Broadcast sends the magic packet from this machine to each address on
// UDP ports 9 and 7. It returns the addresses that accepted the packet.
func Broadcast(mac net.HardwareAddr, addrs []string) ([]string, error) {
	pkt := MagicPacket(mac)
	if len(addrs) == 0 {
		addrs = []string{"255.255.255.255"}
	}
	var sent []string
	var errs []error
	for _, a := range addrs {
		for _, port := range []int{9, 7} {
			conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(a), Port: port})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			_, err = conn.Write(pkt)
			conn.Close()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			sent = append(sent, fmt.Sprintf("%s:%d", a, port))
		}
	}
	if len(sent) == 0 {
		return nil, errors.Join(errs...)
	}
	return sent, nil
}
