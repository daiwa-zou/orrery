package api

import (
	"strings"
	"testing"
)

// render prints a diff the way it reads, so a failure shows the shape.
func render(lines []diffLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(string(l.Op))
		b.WriteString(l.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestLineDiffShowsTheChangedLinesWithContext(t *testing.T) {
	before := []string{"a", "b", "c", "d", "e"}
	after := []string{"a", "b", "X", "d", "e"}

	got := render(lineDiff(before, after, 1))
	want := " b\n-c\n+X\n d\n"
	if got != want {
		t.Errorf("diff =\n%s\nwant\n%s", got, want)
	}
}

func TestLineDiffMarksWhatItLeftOut(t *testing.T) {
	// Two changes far apart: the unchanged middle is dropped, and the fact
	// that it was dropped is on screen rather than implied.
	before := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	after := []string{"X", "2", "3", "4", "5", "6", "7", "8", "Y"}

	got := render(lineDiff(before, after, 1))
	if !strings.Contains(got, "…") {
		t.Errorf("diff =\n%s\nwant a marker for the skipped middle", got)
	}
	if strings.Contains(got, " 5") {
		t.Errorf("diff =\n%s\nwant the far-from-change lines dropped", got)
	}
	// And both ends are still there.
	if !strings.Contains(got, "-1") || !strings.Contains(got, "+Y") {
		t.Errorf("diff =\n%s\nwant both changes shown", got)
	}
}

func TestLineDiffOfEqualInputsIsNothing(t *testing.T) {
	if got := lineDiff([]string{"a", "b"}, []string{"a", "b"}, 2); got != nil {
		t.Errorf("diff = %v, want nothing to show", got)
	}
}

func TestLineDiffHandlesOneSideEmpty(t *testing.T) {
	added := lineDiff(nil, []string{"a", "b"}, 2)
	if len(added) != 2 || added[0].Op != diffAdded || added[1].Op != diffAdded {
		t.Errorf("diff = %v, want both lines added", added)
	}
	removed := lineDiff([]string{"a", "b"}, nil, 2)
	if len(removed) != 2 || removed[0].Op != diffRemoved {
		t.Errorf("diff = %v, want both lines removed", removed)
	}
}

func TestLineDiffRefusesAnInputTooLargeToBeWorthIt(t *testing.T) {
	huge := make([]string, maxDiffInput+1)
	for i := range huge {
		huge[i] = "line"
	}
	if got := lineDiff(huge, []string{"one"}, 2); got != nil {
		t.Error("a template past the cap should not be diffed at all")
	}
}

func TestTruncateDiffCountsWhatItDropped(t *testing.T) {
	lines := []diffLine{
		{Op: diffContext, Text: "a"},
		{Op: diffRemoved, Text: "b"},
		{Op: diffAdded, Text: "c"},
		{Op: diffRemoved, Text: "d"},
	}

	kept, dropped := truncateDiff(lines, 4)
	if len(kept) != 4 || dropped != 0 {
		t.Errorf("truncateDiff kept %d dropped %d, want everything", len(kept), dropped)
	}

	// Only the changed lines are counted: "and 1 more line of context" is not
	// something anyone needs to know.
	kept, dropped = truncateDiff(lines, 2)
	if len(kept) != 2 {
		t.Errorf("kept %d lines, want 2", len(kept))
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want the 2 changed lines counted", dropped)
	}
}
