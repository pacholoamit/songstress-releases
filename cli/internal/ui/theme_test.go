package ui

import (
	"strings"
	"testing"
)

func TestBannerContainsWordmarkAndNoTabs(t *testing.T) {
	b := Banner()
	if !strings.Contains(stripANSI(b), "S O N G S T R E S S") {
		t.Fatalf("banner missing wordmark: %q", b)
	}
	if strings.Contains(b, "\t") {
		t.Fatal("banner must not contain tabs (breaks alignment)")
	}
}

// stripANSI removes CSI sequences so the wordmark is matchable when the
// banner renders with per-rune colors.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
