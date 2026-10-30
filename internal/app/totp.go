package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"rsc.io/qr"
)

// Wake codes over radio are TOTP codes (RFC 6238: HMAC-SHA-1, 6 digits,
// 30-second steps), the kind any authenticator app shows, even on a phone
// with no signal.

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

func totpCode(key []byte, step int64) string {
	mac := hmac.New(sha1.New, key)
	_ = binary.Write(mac, binary.BigEndian, uint64(step))
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// totpCheck reports whether code is right at t, allowing one step either
// side for clocks that are a little off, and which step it was for.
func totpCheck(secret, code string, t time.Time) (int64, bool) {
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil || len(code) != 6 {
		return 0, false
	}
	now := t.Unix() / 30
	for _, step := range []int64{now - 1, now, now + 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func totpURI(secret string) string {
	return "otpauth://totp/Sphynx:radio%20wake?secret=" + secret + "&issuer=Sphynx&algorithm=SHA1&digits=6&period=30"
}

// qrDataURL draws text as a QR code, as an SVG data URL.
func qrDataURL(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	n := c.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(b.String())), nil
}
