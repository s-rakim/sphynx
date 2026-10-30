package chat

import (
	"strings"
	"unicode"
)

// isEmoji reports whether text is a single emoji, including ZWJ sequences,
// skin tones, flags and keycaps.
func isEmoji(text string) bool {
	runes := []rune(text)
	if len(runes) == 0 || len(runes) > 16 {
		return false
	}
	hasSymbol := false
	for i, r := range runes {
		switch {
		case unicode.Is(unicode.So, r):
			hasSymbol = true
		case unicode.In(r, unicode.Sk, unicode.Mn, unicode.Me) || r == 0x200D || (r >= 0xE0020 && r <= 0xE007F):
			continue
		case strings.ContainsRune("0123456789#*", r) && i == 0 && len(runes) > 1 && runes[len(runes)-1] == 0x20E3:
			hasSymbol = true // keycap
		default:
			return false
		}
	}
	return hasSymbol
}
