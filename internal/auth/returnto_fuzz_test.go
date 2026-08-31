package auth

// safeReturnTo is the open-redirect defence on the login flow: whatever a
// caller puts in ?returnTo=, the browser is sent there afterwards. It is
// hand-rolled — a prefix check, a backslash check, a control-character sweep
// and a url.Parse — and every one of those is a rule about how a *browser*
// reads a string, which is not a thing a reading of the function can settle.
//
// So it is fuzzed against the property it exists to guarantee rather than
// against a list of the tricks already thought of: whatever comes back must be
// a path on this origin after the browser has finished normalising it.

import (
	"net/url"
	"strings"
	"testing"
)

// browserNormalise applies the two transformations a browser performs before
// it resolves a URL, and which therefore happen *after* this function has
// approved a string: ASCII tab, CR and LF are stripped out entirely, and a
// backslash in the authority position is read as a slash.
func browserNormalise(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, v)
	return strings.ReplaceAll(v, "\\", "/")
}

func FuzzSafeReturnTo(f *testing.F) {
	for _, seed := range []string{
		"", "/", "/c/lens-a/r/core/v1/pods",
		"//evil.example", "/\\evil.example", "/\t/evil.example",
		"https://evil.example", "http:/\\/\\evil.example",
		"/path?q=1#frag", "javascript:alert(1)", "\r\n/ok",
		"/%2f%2fevil.example", "///evil.example", "/.//evil.example",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got := safeReturnTo(in)

		// The home fallback is always acceptable.
		if got == "/" {
			return
		}

		// Anything else is handed to http.Redirect verbatim, so it has to be a
		// same-origin path once the browser has had it.
		if !strings.HasPrefix(got, "/") {
			t.Fatalf("safeReturnTo(%q) = %q, which is not a path", in, got)
		}
		norm := browserNormalise(got)
		if strings.HasPrefix(norm, "//") {
			t.Fatalf("safeReturnTo(%q) = %q, protocol-relative once normalised (%q)", in, got, norm)
		}
		u, err := url.Parse(norm)
		if err != nil {
			t.Fatalf("safeReturnTo(%q) = %q, which does not parse after normalising: %v", in, got, err)
		}
		if u.Scheme != "" || u.Host != "" {
			t.Fatalf("safeReturnTo(%q) = %q, which resolves off-origin (scheme=%q host=%q)",
				in, got, u.Scheme, u.Host)
		}
	})
}
