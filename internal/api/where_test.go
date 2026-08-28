package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

var whereCols = []Column{
	{Key: "name", Label: "Name", Type: ColText},
	{Key: "status", Label: "Status", Type: ColStatus},
	{Key: "restarts", Label: "Restarts", Type: ColNumber},
	{Key: "age", Label: "Age", Type: ColAge},
	{Key: "ports", Label: "Ports", Type: ColList},
}

func mustParseWhere(t *testing.T, terms ...string) []wherePredicate {
	t.Helper()
	preds, err := parseWhere(terms, whereCols)
	if err != nil {
		t.Fatalf("parseWhere(%q): %v", terms, err)
	}
	return preds
}

func TestWhereNumericComparison(t *testing.T) {
	row := map[string]any{"restarts": int64(3)}

	cases := []struct {
		term string
		want bool
	}{
		{"restarts>2", true},
		{"restarts>3", false},
		{"restarts>=3", true},
		{"restarts<4", true},
		{"restarts<3", false},
		{"restarts<=3", true},
	}
	for _, c := range cases {
		if got := matchesAll(mustParseWhere(t, c.term), row); got != c.want {
			t.Errorf("%s against restarts=3 = %v, want %v", c.term, got, c.want)
		}
	}
}

// ">=" must not be read as ">" followed by a value starting "=", which would
// compare against an unparseable number and refuse a perfectly good term.
func TestWhereReadsTheLongerOperator(t *testing.T) {
	preds := mustParseWhere(t, "restarts>=2")
	if preds[0].op != opGTE || preds[0].value != "2" {
		t.Fatalf("parsed as op %q value %q, want >= and 2", preds[0].op, preds[0].value)
	}
}

func TestWhereNumberAcceptsQuantities(t *testing.T) {
	// Plain integers and Kubernetes quantities are both numbers to a reader.
	preds := mustParseWhere(t, "restarts>1k")
	if preds[0].num != 1000 {
		t.Errorf("1k parsed as %v, want 1000", preds[0].num)
	}
}

func TestWherePatternMatching(t *testing.T) {
	row := map[string]any{"name": "web-canary-1", "status": "Running"}

	for term, want := range map[string]bool{
		`name=~^web-`:      true,
		`name=~^api-`:      false,
		`name!~canary`:     false,
		`name!~nomatch`:    true,
		`name=~canary$|1$`: true,
		`status=~^Run`:     true,
	} {
		if got := matchesAll(mustParseWhere(t, term), row); got != want {
			t.Errorf("%s against %v = %v, want %v", term, row, got, want)
		}
	}
}

// A comma inside a pattern is why each predicate is its own parameter rather
// than one comma-separated list.
func TestWherePatternMayContainCommas(t *testing.T) {
	preds := mustParseWhere(t, "name=~^web-[0-9]{2,3}$")
	row := map[string]any{"name": "web-123"}
	if !preds[0].matches(row) {
		t.Errorf("pattern with a comma did not match %v", row)
	}
	if preds[0].matches(map[string]any{"name": "web-1"}) {
		t.Error("pattern matched a name it should not have")
	}
}

// The cell is a timestamp and the bound is an age. Comparing the timestamp
// directly rather than the age inverts every one of these, which is the whole
// reason this has its own test.
func TestWhereAgeComparesAgeNotTimestamp(t *testing.T) {
	twoHoursOld := map[string]any{"age": time.Now().Add(-2 * time.Hour).Format(time.RFC3339)}
	brandNew := map[string]any{"age": time.Now().Format(time.RFC3339)}

	older := mustParseWhere(t, "age>1h")
	if !matchesAll(older, twoHoursOld) {
		t.Error("age>1h did not match an object two hours old")
	}
	if matchesAll(older, brandNew) {
		t.Error("age>1h matched an object created just now")
	}

	younger := mustParseWhere(t, "age<1h")
	if !matchesAll(younger, brandNew) {
		t.Error("age<1h did not match an object created just now")
	}
	if matchesAll(younger, twoHoursOld) {
		t.Error("age<1h matched an object two hours old")
	}
}

func TestWhereAgeUnits(t *testing.T) {
	for s, want := range map[string]time.Duration{
		"30s": 30 * time.Second,
		"5m":  5 * time.Minute,
		"2h":  2 * time.Hour,
		"3d":  72 * time.Hour,
		"1w":  7 * 24 * time.Hour,
	} {
		got, err := parseAgeBound(s)
		if err != nil {
			t.Errorf("parseAgeBound(%q): %v", s, err)
			continue
		}
		if got != want {
			t.Errorf("parseAgeBound(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestWhereListColumnIsSearchableByPattern(t *testing.T) {
	row := map[string]any{"ports": []any{"80/TCP", "443/TCP"}}
	if !matchesAll(mustParseWhere(t, "ports=~443"), row) {
		t.Error("a pattern could not reach an entry of a list column")
	}
}

// An absent cell has not answered the question, including a negative one.
// Passing it would quietly widen every !~ filter.
func TestWhereAbsentCellNeverMatches(t *testing.T) {
	empty := map[string]any{}
	for _, term := range []string{"restarts>0", "name=~.", "name!~nothing", "age<1h"} {
		if matchesAll(mustParseWhere(t, term), empty) {
			t.Errorf("%s matched a row with no such cell", term)
		}
	}
}

func TestWhereRefusesWhatItCannotAnswer(t *testing.T) {
	cases := []struct {
		term string
		want string
	}{
		{"restarts", "no comparison"},
		{">3", "does not name a column"},
		{"restarts>", "nothing to compare"},
		{"nosuch>1", `no column "nosuch"`},
		{"restarts>abc", "is not a number"},
		{"age>banana", "is not a duration"},
		{"name>abc", "cannot order"},
		{"status<x", "cannot order"},
		{"name=~[unclosed", "not a valid pattern"},
	}
	for _, c := range cases {
		_, err := parseWhere([]string{c.term}, whereCols)
		if err == nil {
			t.Errorf("%s was accepted; expected a refusal mentioning %q", c.term, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s refused with %q, want it to mention %q", c.term, err, c.want)
		}
	}
}

// The refusal for an unknown column has to be usable: it names what there is.
func TestWhereUnknownColumnListsTheRealOnes(t *testing.T) {
	_, err := parseWhere([]string{"restart>1"}, whereCols)
	if err == nil {
		t.Fatal("a misspelt column was accepted")
	}
	for _, want := range []string{"restarts", "age", "name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention the %q column", err, want)
		}
	}
}

// A label selector's "!=" must keep its meaning: nothing here may claim it.
func TestWhereDoesNotClaimSelectorOperators(t *testing.T) {
	for _, term := range []string{"tier!=cache", "app=web"} {
		if _, err := parseWhere([]string{term}, whereCols); err == nil {
			t.Errorf("%s was parsed as a where predicate; it is a label selector", term)
		}
	}
}

func TestWhereEmptyInputIsNoFilter(t *testing.T) {
	preds, err := parseWhere(nil, whereCols)
	if err != nil || preds != nil {
		t.Errorf("parseWhere(nil) = %v, %v; want no predicates and no error", preds, err)
	}
	preds, err = parseWhere([]string{"", "  "}, whereCols)
	if err != nil || len(preds) != 0 {
		t.Errorf("blank terms = %v, %v; want none", preds, err)
	}
}

// End to end over the list endpoint: the predicate has to reach the rows, the
// count and the watch's idea of what matches, not just the parser.
func TestWhereFiltersTheListEndpoint(t *testing.T) {
	rig := hndNewRig(t)

	names := func(query string) []string {
		t.Helper()
		rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods"+query)
		hndWantStatus(t, rec, http.StatusOK)
		var page struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if page.Total != len(page.Items) {
			t.Errorf("total %d disagrees with %d items — the filter ran after the count",
				page.Total, len(page.Items))
		}
		out := make([]string, 0, len(page.Items))
		for _, it := range page.Items {
			out = append(out, asString(it["name"]))
		}
		sort.Strings(out)
		return out
	}

	all := names("?namespace=demo")
	if len(all) < 3 {
		t.Fatalf("fixture has %d pods, too few to filter meaningfully", len(all))
	}

	web := names("?namespace=demo&where=name%3D~%5Eweb-")
	if len(web) == 0 || len(web) == len(all) {
		t.Fatalf("name=~^web- selected %v out of %v; expected a proper subset", web, all)
	}
	for _, n := range web {
		if !strings.HasPrefix(n, "web-") {
			t.Errorf("name=~^web- returned %q", n)
		}
	}

	// The negation is the complement, which is the useful property to hold.
	notWeb := names("?namespace=demo&where=name%21~%5Eweb-")
	if len(web)+len(notWeb) != len(all) {
		t.Errorf("=~ selected %v and !~ selected %v; together they should be all %v",
			web, notWeb, all)
	}

	// Two predicates are an AND, and each is its own parameter.
	both := names("?namespace=demo&where=name%3D~%5Eweb-&where=name%3D~1%24")
	for _, n := range both {
		if !strings.HasPrefix(n, "web-") || !strings.HasSuffix(n, "1") {
			t.Errorf("two predicates returned %q, which fails one of them", n)
		}
	}
}

func TestWhereRefusalIsA400WithAReason(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods?where=nosuch%3E1")
	hndWantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "nosuch") {
		t.Errorf("the refusal does not name the column: %s", rec.Body.String())
	}

	// A text column ordered rather than matched is refused too, rather than
	// being answered lexicographically.
	rec = rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods?where=name%3Eabc")
	hndWantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "=~") {
		t.Errorf("the refusal does not suggest the operator that would work: %s", rec.Body.String())
	}
}

// Autocomplete narrows by the search, and a column predicate is part of the
// search: suggesting values that the predicate excludes would be the same
// dead end the narrowing was added to remove.
func TestWhereNarrowsTheFacets(t *testing.T) {
	rig := hndNewRig(t)

	vocabulary := func(query string) facetsResponse {
		t.Helper()
		rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods/facets"+query)
		hndWantStatus(t, rec, http.StatusOK)
		var resp facetsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	all := vocabulary("?namespace=demo")
	narrowed := vocabulary("?namespace=demo&where=name%3D~%5Eweb-")
	if len(valuesFor(all.Fields, "status.phase")) == 0 {
		t.Skip("fixture has no phases to narrow")
	}
	if len(valuesFor(narrowed.Fields, "status.phase")) > len(valuesFor(all.Fields, "status.phase")) {
		t.Error("narrowing by a predicate widened the vocabulary")
	}
}
