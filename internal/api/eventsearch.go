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

// maxSearchTerms bounds how many words one box may contain.
//
// The terms multiply: every one of them is looked for in every searched column
// of every event in scope, and the scan runs before the limit — deliberately,
// so that a match older than the newest few hundred still surfaces. That makes
// the parameter a product of three numbers, and it is the only one of the three
// the caller sets. Words are separated by a single space, so a megabyte of
// query string is some five hundred thousand of them, against the tens of
// thousands of events a busy cluster's informer holds; the scan is then
// thousands of seconds long and there is nothing to interrupt it, since the
// loop never consults the request context and writeTimeout is deliberately
// zero.
//
// Sixteen is past what a search box is for. The queries this grammar was built
// around — "back-off web", "failed mount config" — are two and three words, and
// a person who needs more than sixteen wants `where` predicates, which is what
// they are there for.
const maxSearchTerms = 16

// parseSearchTerms splits free text into the terms every row must satisfy.
//
// Quotes group words that belong together — a message is prose, and
// "back-off restarting" is one thing to look for rather than two. An
// unterminated quote runs to the end of the input, because it is being typed
// and the alternative is the results flickering to nothing on the keystroke
// that opens it.
func parseSearchTerms(q string) ([]searchTerm, error) {
	var terms []searchTerm
	// Refused rather than truncated. Dropping the seventeenth word answers a
	// question nobody asked and answers it with *more* rows than the one they
	// did ask, which is the direction a filter must never be wrong in.
	tooMany := false
	var cur strings.Builder
	negate := false
	quoted := false
	// True at the point where a "-" is still an operator rather than a
	// hyphen: "back-off" is one word, "-Pulled" is an exclusion.
	fresh := true

	flush := func() {
		if cur.Len() > 0 {
			if len(terms) == maxSearchTerms {
				tooMany = true
			} else {
				terms = append(terms, searchTerm{text: strings.ToLower(cur.String()), negate: negate})
			}
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
	if tooMany {
		return nil, badRequest(
			"at most %d search words per request; narrow the text or use `where` predicates",
			maxSearchTerms)
	}
	return terms, nil
}

// rowMatchesSearch reports whether one projected row satisfies every term.
//
// A negated term is satisfied by absence, so "-Pulled" quietens a noisy feed
// instead of emptying it.
//
// The row's cells are folded once and then all the terms are run over them.
// Asking rowMatchesQuery per term instead re-lowered the same message for every
// word in the box — a fresh allocation each time, terms times keys of them per
// event — so the cost of a second search word was not one more comparison but
// another whole pass of copies over the feed.
func rowMatchesSearch(row map[string]any, terms []searchTerm, keys []string) bool {
	if len(terms) == 0 {
		return true
	}
	// Sized for eventSearchKeys, which is the only caller; a longer key list
	// grows it once rather than escaping to the heap per row.
	var buf [8]string
	cells := buf[:0]
	for _, k := range keys {
		if v, ok := row[k]; ok {
			cells = append(cells, strings.ToLower(asString(v)))
		}
	}
	for _, t := range terms {
		found := false
		for _, c := range cells {
			if strings.Contains(c, t.text) {
				found = true
				break
			}
		}
		if found == t.negate {
			return false
		}
	}
	return true
}
