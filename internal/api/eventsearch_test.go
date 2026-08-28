package api

import "testing"

func TestParseSearchTerms(t *testing.T) {
	cases := []struct {
		in   string
		want []searchTerm
	}{
		{"", nil},
		{"   ", nil},
		// The whole point: two words are two requirements, not one string.
		{"back-off web", []searchTerm{{text: "back-off"}, {text: "web"}}},
		// A hyphen inside a word is a hyphen; one starting a word excludes.
		{"-Pulled", []searchTerm{{text: "pulled", negate: true}}},
		{"back-off -pulled", []searchTerm{{text: "back-off"}, {text: "pulled", negate: true}}},
		// A phrase is one term, quotes and all removed.
		{`"failed to mount"`, []searchTerm{{text: "failed to mount"}}},
		{`-"image pull"`, []searchTerm{{text: "image pull", negate: true}}},
		{`web "failed to mount"`, []searchTerm{{text: "web"}, {text: "failed to mount"}}},
		// A quote that is still being typed runs to the end rather than
		// emptying the list on the keystroke that opened it.
		{`"failed to`, []searchTerm{{text: "failed to"}}},
		// A lone operator is not a term at all.
		{"-", nil},
		{`""`, nil},
	}

	for _, c := range cases {
		got := parseSearchTerms(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseSearchTerms(%q) = %+v, want %+v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseSearchTerms(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestRowMatchesSearch(t *testing.T) {
	row := map[string]any{
		"object":  "Pod/web-abc",
		"reason":  "BackOff",
		"message": "Back-off restarting failed container",
		"type":    "Warning",
		"count":   int64(7),
	}

	match := func(q string) bool {
		return rowMatchesSearch(row, parseSearchTerms(q), eventSearchKeys)
	}

	// Words are ANDed across columns: "web" is in the object and "back-off"
	// is in the message, and the row satisfies both.
	if !match("web back-off") {
		t.Error("words spread across columns should match")
	}
	if match("web postgres") {
		t.Error("every word is required, not any")
	}
	if !match("warning") {
		t.Error("the type is searchable, so a bare warning finds warnings")
	}
	// Exclusion is satisfied by absence, and not by the presence of the rest.
	if !match("back-off -postgres") {
		t.Error("an absent exclusion should leave the row matching")
	}
	if match("back-off -web") {
		t.Error("a present exclusion should drop the row")
	}
	// A phrase is contiguous; its words apart are not it.
	if !match(`"restarting failed"`) {
		t.Error("a phrase present in the message should match")
	}
	if match(`"failed restarting"`) {
		t.Error("a phrase is not its words in any order")
	}
	// Nothing typed asks nothing of the row.
	if !match("") {
		t.Error("an empty search should not filter")
	}
}
