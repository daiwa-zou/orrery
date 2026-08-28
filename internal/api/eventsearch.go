package api

import (
	"strings"
	"unicode"
)

// The event feed's free-text search.
//
// Events are the one list where what a reader types is a sentence rather than
// a name: "back-off web", "failed mount config". A single Contains over one
// cell cannot answer that — the words are spread across the object, the reason
// and the message — so the box used to answer "no matches" to queries whose
// answer was plainly on the screen behind it.
//
// The grammar is the one every search box has, and nothing more: words are
// ANDed, each may land in a different column, a quoted phrase is one word, and
// a leading "-" excludes. The structured questions — only warnings, seen more
// than three times, in the last quarter hour — are asked with the `where`
// predicates every other list already accepts, rather than with a second
// grammar invented here.

// searchTerm is one word or phrase from the box, lower-cased, and whether the
// row must *not* contain it.
type searchTerm struct {
	text   string
	negate bool
}

// parseSearchTerms splits free text into the terms every row must satisfy.
//
// Quotes group words that belong together — a message is prose, and
// "back-off restarting" is one thing to look for rather than two. An
// unterminated quote runs to the end of the input, because it is being typed
// and the alternative is the results flickering to nothing on the keystroke
// that opens it.
func parseSearchTerms(q string) []searchTerm {
	var terms []searchTerm
	var cur strings.Builder
	negate := false
	quoted := false
	// True at the point where a "-" is still an operator rather than a
	// hyphen: "back-off" is one word, "-Pulled" is an exclusion.
	fresh := true

	flush := func() {
		if cur.Len() > 0 {
			terms = append(terms, searchTerm{text: strings.ToLower(cur.String()), negate: negate})
			cur.Reset()
		}
		negate = false
		fresh = true
	}

	for _, r := range q {
		switch {
		case r == '"':
			quoted = !quoted
			fresh = false
		case !quoted && unicode.IsSpace(r):
			flush()
		case fresh && r == '-':
			negate = true
			fresh = false
		default:
			fresh = false
			cur.WriteRune(r)
		}
	}
	flush()
	return terms
}

// rowMatchesSearch reports whether one projected row satisfies every term.
//
// A negated term is satisfied by absence, so "-Pulled" quietens a noisy feed
// instead of emptying it.
func rowMatchesSearch(row map[string]any, terms []searchTerm, keys []string) bool {
	for _, t := range terms {
		if rowMatchesQuery(row, t.text, keys) == t.negate {
			return false
		}
	}
	return true
}
