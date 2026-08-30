package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The reason on a close frame is the last thing a stream says. It is also
// size-limited and required to be valid UTF-8, and it carries API server error
// text — so the two constraints meet in a place where getting it wrong turns
// "your access was revoked" into a protocol error and no explanation at all.
func TestCloseReasonFitsTheFrame(t *testing.T) {
	short := "access to this pod was revoked"
	if got := closeReason(short); got != short {
		t.Errorf("closeReason(%q) = %q, want it untouched", short, got)
	}

	// A rune that straddles the cut. The 'é' is two bytes, so a run of them
	// puts a partial rune at every odd boundary.
	long := strings.Repeat("é", 200)
	got := closeReason(long)
	if len(got) > maxCloseReason {
		t.Errorf("closeReason returned %d bytes, want at most %d", len(got), maxCloseReason)
	}
	if !utf8.ValidString(got) {
		t.Errorf("closeReason returned invalid UTF-8: %q", got)
	}
	if got == "" {
		t.Error("closeReason threw the whole sentence away")
	}

	// Plain ASCII is cut at the limit and stays whole.
	ascii := strings.Repeat("a", 200)
	if got := closeReason(ascii); len(got) != maxCloseReason {
		t.Errorf("closeReason(ascii) = %d bytes, want %d", len(got), maxCloseReason)
	}
}
