package api

// The two grammars this server exposes to free text: `?where=` column
// predicates and the event feed's search box. Both are hand-written scanners
// over a string a caller controls entirely, both compile that string into
// something later run against every object in scope, and both have bounds whose
// whole purpose is to hold under input nobody anticipated.
//
// Fuzzed against their contracts rather than against a list of the inputs
// already thought of — the bounds especially, since a cap that a crafted string
// can slip past is not a cap.

import (
	"strings"
	"testing"
)

func FuzzParseWhere(f *testing.F) {
	for _, seed := range []string{
		"restarts>3", "age<1h", "name=~^web-", "status!~Running",
		"restarts>=0", "age>3d", "ports=~443", "name=~x{2,3}",
		"", ">", "=~", "restarts>", "nosuch>1", "name=~[unclosed",
		"restarts>nan", "age>infd", "restarts>1e400", "a>=~b",
		"name=~" + strings.Repeat("a", 300),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		preds, err := parseWhere([]string{in}, whereCols)
		if err != nil {
			if preds != nil {
				t.Fatalf("parseWhere(%q) returned %d predicates alongside an error", in, len(preds))
			}
			return
		}

		// A predicate that parsed must be runnable against any row without
		// blowing up — it is applied to every object in scope, and the cells
		// come from the cluster rather than from the caller.
		rows := []map[string]any{
			{},
			{"name": "web-1", "status": "Running", "restarts": int64(3), "age": "2024-01-01T00:00:00Z"},
			{"name": nil, "restarts": "not-a-number", "age": "not-a-time", "ports": []any{"80/TCP", nil}},
			{"restarts": 1.5, "status": []string{"a", "b"}, "age": 42},
		}
		for _, row := range rows {
			_ = matchesAll(preds, row)
		}

		// The pattern budget is the bound that stops one term carrying the
		// whole request line into a per-object regexp match.
		budget := 0
		for _, p := range preds {
			if p.re == nil {
				continue
			}
			if len(p.value) > maxPatternBytes {
				t.Fatalf("parseWhere(%q) accepted a %d-byte pattern, over the %d cap",
					in, len(p.value), maxPatternBytes)
			}
			budget += len(p.value)
		}
		if budget > maxPatternBudget {
			t.Fatalf("parseWhere(%q) accepted %d pattern bytes, over the %d budget",
				in, budget, maxPatternBudget)
		}
	})
}

func FuzzParseSearchTerms(f *testing.F) {
	for _, seed := range []string{
		"", "back-off web", `"failed mount" config`, "-Pulled",
		`-"back-off restarting"`, `unterminated "quote`, "   ",
		"-", "--", `"`, `""`, "a\tb", strings.Repeat("w ", 40),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		terms, err := parseSearchTerms(in)
		if err != nil {
			if terms != nil {
				t.Fatalf("parseSearchTerms(%q) returned %d terms alongside an error", in, len(terms))
			}
			return
		}
		// Refused rather than truncated is the documented contract: dropping a
		// word answers with *more* rows than were asked for, which is the one
		// direction a filter must never be wrong in.
		if len(terms) > maxSearchTerms {
			t.Fatalf("parseSearchTerms(%q) returned %d terms, over the %d cap",
				in, len(terms), maxSearchTerms)
		}
		for _, term := range terms {
			if term.text == "" {
				t.Fatalf("parseSearchTerms(%q) produced an empty term, which matches every row", in)
			}
			if term.text != strings.ToLower(term.text) {
				t.Fatalf("parseSearchTerms(%q) produced un-folded term %q; "+
					"rowMatchesSearch folds the cells only", in, term.text)
			}
		}
		// Runnable against any row.
		_ = rowMatchesSearch(map[string]any{
			"object": "Pod/web-1", "reason": "BackOff", "message": nil, "type": 7,
		}, terms, eventSearchKeys)
	})
}
