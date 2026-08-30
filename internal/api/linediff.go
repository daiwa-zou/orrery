package api

// A line diff between two revisions of a pod template.
//
// Naming the fields that differ — "args", "env" — tells a reader whether to
// look. It does not tell them what they would be going back to, and that is
// the actual question in front of someone choosing a rollback target: not
// "did the arguments change" but "which argument, and to what". So the rows
// carry the lines themselves.
//
// The diff is computed here rather than in the browser because the templates
// are already in hand server-side, and shipping two full pod templates per
// revision to diff them again in the client would be several times the payload
// for the same answer.
//
// Written out rather than taken from a library: the only diff implementation
// already in the module is go-difflib, which is testify's transitive
// dependency, and promoting a test library's dependency into the serving path
// to save sixty lines is a poor trade.

// diffOp marks what happened to a line.
type diffOp string

const (
	diffContext diffOp = " "
	diffRemoved diffOp = "-"
	diffAdded   diffOp = "+"
	// diffSkipped stands in for the unchanged lines between two hunks, so a
	// reader can see that something was left out rather than assume the two
	// halves are adjacent.
	diffSkipped diffOp = "…"
)

// diffLine is one line of the rendered diff.
type diffLine struct {
	Op   diffOp `json:"op"`
	Text string `json:"text"`
}

// maxDiffInput caps what will be diffed at all. The table is quadratic, and a
// pod template large enough to matter is one nobody is reading in a modal
// anyway.
const maxDiffInput = 2000

// lineDiff renders the change from `before` to `after` as a unified diff with
// `context` unchanged lines around each hunk.
//
// Returns nil when there is nothing to show — equal inputs, or an input too
// large to be worth the table. Nil is distinguishable from an empty diff by
// the caller having compared the two already.
func lineDiff(before, after []string, context int) []diffLine {
	if len(before) > maxDiffInput || len(after) > maxDiffInput {
		return nil
	}

	// Longest common subsequence, built from the end so the walk forward reads
	// in document order.
	//
	// One flat table rather than a slice of rows. The fill and the walk both
	// read lcs[i+1][j] next to lcs[i][j+1], so a row-major array keeps the two
	// neighbours near each other instead of behind a pointer each; and int32
	// is more than enough for a count that cannot exceed maxDiffInput. The
	// row-of-slices shape allocated a header and a backing array per row —
	// two thousand allocations and twice the bytes at the cap — every time a
	// rollout page asked for one more revision's diff.
	stride := len(after) + 1
	lcs := make([]int32, (len(before)+1)*stride)

	for i := len(before) - 1; i >= 0; i-- {
		row, next := i*stride, (i+1)*stride
		for j := len(after) - 1; j >= 0; j-- {
			switch {
			case before[i] == after[j]:
				lcs[row+j] = lcs[next+j+1] + 1
			case lcs[next+j] >= lcs[row+j+1]:
				lcs[row+j] = lcs[next+j]
			default:
				lcs[row+j] = lcs[row+j+1]
			}
		}
	}

	var full []diffLine
	i, j := 0, 0
	for i < len(before) && j < len(after) {
		switch {
		case before[i] == after[j]:
			full = append(full, diffLine{Op: diffContext, Text: before[i]})
			i++
			j++
		case lcs[(i+1)*stride+j] >= lcs[i*stride+j+1]:
			full = append(full, diffLine{Op: diffRemoved, Text: before[i]})
			i++
		default:
			full = append(full, diffLine{Op: diffAdded, Text: after[j]})
			j++
		}
	}
	for ; i < len(before); i++ {
		full = append(full, diffLine{Op: diffRemoved, Text: before[i]})
	}
	for ; j < len(after); j++ {
		full = append(full, diffLine{Op: diffAdded, Text: after[j]})
	}

	return withContext(full, context)
}

// withContext drops the unchanged lines that are far from any change, leaving
// a marker where they were.
func withContext(full []diffLine, context int) []diffLine {
	keep := make([]bool, len(full))
	changed := false
	for i, line := range full {
		if line.Op == diffContext {
			continue
		}
		changed = true
		for k := i - context; k <= i+context; k++ {
			if k >= 0 && k < len(full) {
				keep[k] = true
			}
		}
	}
	if !changed {
		return nil
	}

	out := make([]diffLine, 0, len(full))
	skipping := false
	for i, line := range full {
		if keep[i] {
			out = append(out, line)
			skipping = false
			continue
		}
		// One marker per run of dropped lines, and none at the very top or
		// bottom: leading and trailing sameness is not information.
		if !skipping && len(out) > 0 {
			out = append(out, diffLine{Op: diffSkipped})
			skipping = true
		}
	}
	// A trailing marker says "and more of the same", which is true but not
	// worth a row.
	if n := len(out); n > 0 && out[n-1].Op == diffSkipped {
		out = out[:n-1]
	}
	return out
}

// truncateDiff caps a diff at max lines, replacing the tail with a marker that
// counts what was dropped. A cell that silently ends mid-diff reads as a
// complete one.
func truncateDiff(lines []diffLine, max int) ([]diffLine, int) {
	if len(lines) <= max {
		return lines, 0
	}
	dropped := 0
	for _, line := range lines[max:] {
		if line.Op == diffAdded || line.Op == diffRemoved {
			dropped++
		}
	}
	return lines[:max], dropped
}
