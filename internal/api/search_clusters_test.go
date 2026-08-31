package api

// ?cluster= narrows a fleet search, and a value that names no configured
// cluster used to narrow it to nothing in silence: no hits, no warnings, an
// empty `scanned`, and a 200. searchResponse says of its warnings that they
// exist "so 'no results' is never confused with 'nowhere to look'", and this
// was the one route to the second that produced neither.
//
// The names come out of alerts and shell history, so a fleet holding
// `prod-eu-1` will be asked about `prod-eu1`. What came back was that the
// object is not in that cluster — the very thing the caller was searching to
// rule out.

import (
	"strings"
	"testing"
)

func TestSearchNamesAClusterItDoesNotHave(t *testing.T) {
	rig := hndNewRig(t)

	got := search(t, rig, "q=web&cluster=elsewhere")

	// Still not an error: naming a cluster that has left the fleet is a
	// reasonable question about the rest.
	if len(got.Hits) != 0 {
		t.Errorf("hits came back from a cluster that does not exist: %v", got.names())
	}
	if len(got.Warnings) == 0 {
		t.Fatal("nothing was searched and nothing said so; " +
			"an empty result reads as 'your object is not there'")
	}
	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, "elsewhere") {
		t.Errorf("warnings = %q, want them to name the value that matched nothing", joined)
	}
	// Useful enough to act on: say what there is.
	if !strings.Contains(joined, "fake") {
		t.Errorf("warnings = %q, want them to name the configured clusters", joined)
	}
}

// The narrow half of the same bug, and the more dangerous one. "No cluster
// matched" and "no cluster filter was given" are different, and a filter
// tested by its length collapses them — so one misspelt name would have
// searched the whole fleet instead of none of it.
func TestSearchDoesNotWidenWhenTheFilterMatchesNothing(t *testing.T) {
	rig := hndNewRig(t)

	got := search(t, rig, "q=web&cluster=elsewhere")
	if len(got.Scanned) != 0 {
		t.Errorf("scanned %v after naming only an unknown cluster; "+
			"a filter that selects nothing must not read as no filter", got.Scanned)
	}
}

// A recognised name alongside an unrecognised one still searches the
// recognised one, and still says what it skipped.
func TestSearchKeepsTheClustersItDoesHave(t *testing.T) {
	rig := hndNewRig(t)

	got := search(t, rig, "q=web&cluster=fake&cluster=elsewhere")
	if len(got.Hits) == 0 {
		t.Error("a known cluster was dropped because an unknown one was named beside it")
	}
	if len(got.Warnings) == 0 {
		t.Error("the skipped cluster was not reported")
	}
}

// A blank value is how a cleared filter arrives from a form, and it narrows
// nothing rather than narrowing to nothing.
func TestSearchTreatsABlankClusterFilterAsNoFilter(t *testing.T) {
	rig := hndNewRig(t)

	got := search(t, rig, "q=web&cluster=")
	if len(got.Hits) == 0 {
		t.Error("a cleared cluster filter excluded every cluster")
	}
	if len(got.Warnings) != 0 {
		t.Errorf("a cleared filter reported gaps: %v", got.Warnings)
	}
}
