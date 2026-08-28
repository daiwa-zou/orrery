package api

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Predicates over the projected columns: the comparisons a Kubernetes
// selector cannot express.
//
// A label selector answers "is this value exactly that", which is the wrong
// shape for most of what people actually ask a list of workloads — "which
// pods have restarted more than three times", "which of these is older than a
// day", "the web-* deployments but not the canaries". Those are ordering and
// pattern questions, and apimachinery's selectors have no operator for
// either.
//
// So they live in their own `where` parameter rather than being smuggled into
// labelSelector, which is documented as, parsed as, and must remain a
// Kubernetes selector. The operators are chosen so the two can never be
// confused: >, >=, <, <=, =~ and !~ are all syntax errors to labels.Parse, so
// a term using one is unambiguously ours and a term using = still means
// exactly what it always meant.
//
// Each predicate is its own repeated query parameter — ?where=a>1&where=b<2
// — rather than a comma-separated list. Regular expressions contain commas
// (`name=~x{2,3}`), and a separator that has to be escaped inside the values
// it separates is a separator that will eventually be got wrong.
//
// They read the *projected row* rather than the object, so what can be
// filtered is what the table actually shows, and the column's declared type
// decides how a value is compared. That is also why a comparison is checked
// against the column it names: `>` on a text column is not a query anyone
// meant to write, and answering it with a lexicographic comparison would be
// worse than saying so.

type whereOp string

const (
	opGT       whereOp = ">"
	opGTE      whereOp = ">="
	opLT       whereOp = "<"
	opLTE      whereOp = "<="
	opMatch    whereOp = "=~"
	opNotMatch whereOp = "!~"
)

// ordering reports whether the operator compares magnitude rather than shape.
func (o whereOp) ordering() bool {
	return o == opGT || o == opGTE || o == opLT || o == opLTE
}

// whereOps are matched longest-first so ">=" is never read as ">" followed by
// a value beginning "=".
var whereOps = []whereOp{opGTE, opLTE, opMatch, opNotMatch, opGT, opLT}

// wherePredicate is one parsed comparison, ready to run against a row.
type wherePredicate struct {
	column string
	op     whereOp
	value  string

	kind ColumnType
	num  float64        // ordering comparisons against a number column
	dur  time.Duration  // ordering comparisons against an age column
	re   *regexp.Regexp // =~ and !~
}

// parseWhere turns the raw ?where= values into predicates bound to the columns
// of the table being listed.
//
// Binding at parse time is what lets an impossible comparison be refused once,
// with a message naming the column and its type, instead of quietly matching
// nothing on every row.
func parseWhere(raw []string, cols []Column) ([]wherePredicate, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	byKey := make(map[string]Column, len(cols))
	for _, c := range cols {
		byKey[c.Key] = c
	}

	out := make([]wherePredicate, 0, len(raw))
	for _, term := range raw {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		p, err := parseWhereTerm(term, byKey, cols)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parseWhereTerm(term string, byKey map[string]Column, cols []Column) (wherePredicate, error) {
	var p wherePredicate

	op, at := findWhereOp(term)
	if at < 0 {
		return p, badRequest("where: %q has no comparison; expected one of >, >=, <, <=, =~, !~", term)
	}
	p.column = strings.TrimSpace(term[:at])
	p.op = op
	p.value = strings.TrimSpace(term[at+len(op):])

	if p.column == "" {
		return p, badRequest("where: %q does not name a column", term)
	}
	if p.value == "" {
		return p, badRequest("where: %q has nothing to compare against", term)
	}

	col, ok := byKey[p.column]
	if !ok {
		return p, badRequest("where: no column %q on this resource (have: %s)",
			p.column, strings.Join(columnKeys(cols), ", "))
	}
	p.kind = col.Type

	if !op.ordering() {
		re, err := regexp.Compile(p.value)
		if err != nil {
			return p, badRequest("where: %q is not a valid pattern: %v", p.value, err)
		}
		p.re = re
		return p, nil
	}

	switch col.Type {
	case ColNumber:
		n, err := parseNumber(p.value)
		if err != nil {
			return p, badRequest("where: %q is not a number, and %q is numeric", p.value, p.column)
		}
		p.num = n
	case ColAge:
		d, err := parseAgeBound(p.value)
		if err != nil {
			return p, badRequest("where: %q is not a duration (try 30s, 5m, 2h, 3d, 1w)", p.value)
		}
		p.dur = d
	default:
		// Lexicographic ordering of a status or a name is not a question
		// anyone asked; refusing is more useful than answering it.
		return p, badRequest("where: %s cannot order the %s column %q — use =~ to match a pattern",
			op, col.Type, p.column)
	}
	return p, nil
}

// findWhereOp locates the first operator in the term, preferring the longest
// match at a given position.
func findWhereOp(term string) (whereOp, int) {
	best := -1
	var found whereOp
	for _, op := range whereOps {
		at := strings.Index(term, string(op))
		if at < 0 {
			continue
		}
		// "!=" is a label selector's operator, not ours: reading the "=" of it
		// as the start of something would steal a term that already has a
		// meaning.
		if op == opMatch && at > 0 && (term[at-1] == '!' || term[at-1] == '<' || term[at-1] == '>') {
			continue
		}
		if best < 0 || at < best || (at == best && len(op) > len(found)) {
			best, found = at, op
		}
	}
	return found, best
}

func columnKeys(cols []Column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Key)
	}
	sort.Strings(out)
	return out
}

// parseNumber accepts plain numbers and Kubernetes quantities alike, so a
// numeric column carrying 100m or 1Gi compares the way its reader expects.
func parseNumber(s string) (float64, error) {
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return q.AsApproximateFloat64(), nil
}

// parseAgeBound reads a duration, extending Go's units with the days and weeks
// that any question about a cluster is actually asked in.
func parseAgeBound(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		v, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(v * float64(24*time.Hour)), nil
	}
	if n, ok := strings.CutSuffix(s, "w"); ok {
		v, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(v * float64(7*24*time.Hour)), nil
	}
	return time.ParseDuration(s)
}

// matches reports whether one projected row satisfies the predicate.
//
// A cell the row does not carry never matches, including under !~. "The ones
// whose name does not look like this" is a question about names, and an object
// with no name column has not answered it — treating absence as a pass would
// quietly widen every negative filter.
func (p wherePredicate) matches(row map[string]any) bool {
	cell, ok := row[p.column]
	if !ok || cell == nil {
		return false
	}

	if p.re != nil {
		m := p.re.MatchString(cellString(cell))
		if p.op == opNotMatch {
			return !m
		}
		return m
	}

	switch p.kind {
	case ColNumber:
		n, ok := cellNumber(cell)
		if !ok {
			return false
		}
		return compare(n, p.num, p.op)
	case ColAge:
		ts, ok := cell.(string)
		if !ok {
			return false
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return false
		}
		// The cell is a timestamp and the bound is an age, so the comparison
		// is between how old the object is and how old the reader asked for.
		// Comparing the timestamp directly would invert every one of these.
		return compare(float64(time.Since(t)), float64(p.dur), p.op)
	}
	return false
}

func compare(a, b float64, op whereOp) bool {
	switch op {
	case opGT:
		return a > b
	case opGTE:
		return a >= b
	case opLT:
		return a < b
	case opLTE:
		return a <= b
	}
	return false
}

func cellNumber(cell any) (float64, bool) {
	switch v := cell.(type) {
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case string:
		n, err := parseNumber(v)
		return n, err == nil
	}
	return 0, false
}

// cellString renders a cell for pattern matching. A list column is joined with
// spaces so a pattern can reach any one of its entries.
func cellString(cell any) string {
	switch v := cell.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, " ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, e := range v {
			parts = append(parts, cellString(e))
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(cell)
}

// matchesAll reports whether a projected row satisfies every predicate.
// Predicates are ANDed: each one narrows what the last left.
func matchesAll(preds []wherePredicate, row map[string]any) bool {
	for _, p := range preds {
		if !p.matches(row) {
			return false
		}
	}
	return true
}

// filterRows keeps the objects whose projected row satisfies every predicate.
func filterRows(
	objs []*unstructured.Unstructured,
	set columnSet,
	preds []wherePredicate,
) []*unstructured.Unstructured {
	if len(preds) == 0 {
		return objs
	}
	out := objs[:0:0]
	for _, o := range objs {
		if matchesAll(preds, set.row(o)) {
			out = append(out, o)
		}
	}
	return out
}
