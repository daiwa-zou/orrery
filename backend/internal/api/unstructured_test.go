package api

import (
	"reflect"
	"testing"
)

func TestTypedAccessorsCollapseToZeroValues(t *testing.T) {
	u := mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"str":    "hello",
			"num":    int64(7),
			"flag":   true,
			"items":  []any{"a", "b"},
			"labels": []any{"x", "y"},
		},
	})

	if got := str(u, "spec", "str"); got != "hello" {
		t.Errorf("str = %q", got)
	}
	if got := str(u, "spec", "missing"); got != "" {
		t.Errorf("missing str = %q, want empty", got)
	}
	// A type mismatch must also collapse to zero, not error out.
	if got := str(u, "spec", "num"); got != "" {
		t.Errorf("str over an int = %q, want empty", got)
	}

	if got := i64(u, "spec", "num"); got != 7 {
		t.Errorf("i64 = %d", got)
	}
	if got := i64(u, "spec", "str"); got != 0 {
		t.Errorf("i64 over a string = %d, want 0", got)
	}

	if v, ok := i64ok(u, "spec", "num"); !ok || v != 7 {
		t.Errorf("i64ok = %d,%v", v, ok)
	}
	if _, ok := i64ok(u, "spec", "missing"); ok {
		t.Error("i64ok reported a missing field as present")
	}

	if !boolean(u, "spec", "flag") {
		t.Error("boolean lost a true value")
	}
	if boolean(u, "spec", "missing") {
		t.Error("missing boolean should be false")
	}

	if got := slice(u, "spec", "items"); len(got) != 2 {
		t.Errorf("slice = %v", got)
	}
	if got := slice(u, "spec", "missing"); got != nil {
		t.Errorf("missing slice = %v, want nil", got)
	}

	if got := strSlice(u, "spec", "labels"); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("strSlice = %v", got)
	}
}

func TestMapAccessors(t *testing.T) {
	m := map[string]any{
		"s":     "text",
		"i":     int64(4),
		"f":     float64(9),
		"b":     true,
		"wrong": []any{},
	}

	if got := mapOf(m); got == nil {
		t.Error("mapOf lost a map")
	}
	if got := mapOf("not a map"); got != nil {
		t.Errorf("mapOf over a string = %v, want nil", got)
	}

	if got := mstr(m, "s"); got != "text" {
		t.Errorf("mstr = %q", got)
	}
	if got := mstr(m, "i"); got != "" {
		t.Errorf("mstr over an int = %q", got)
	}

	// JSON round-trips turn ints into float64; both spellings must count.
	if got := mint(m, "i"); got != 4 {
		t.Errorf("mint(int64) = %d", got)
	}
	if got := mint(m, "f"); got != 9 {
		t.Errorf("mint(float64) = %d", got)
	}
	if got := mint(m, "s"); got != 0 {
		t.Errorf("mint over a string = %d, want 0", got)
	}
	if got := mint(m, "missing"); got != 0 {
		t.Errorf("mint missing = %d, want 0", got)
	}

	if !mbool(m, "b") {
		t.Error("mbool lost a true value")
	}
	if mbool(m, "wrong") {
		t.Error("mbool over a slice should be false")
	}
}

func TestContainerImages(t *testing.T) {
	u := mkObj(t, nil, map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"initContainers": []any{
				map[string]any{"image": "busybox:1.36"},
				map[string]any{"name": "no-image"}, // must be skipped, not rendered as ""
			},
			"containers": []any{
				map[string]any{"image": "nginx:1.27"},
			},
		}}},
	})

	got := containerImages(u, "spec", "template", "spec")
	// Init containers come first, matching how the pod actually runs.
	want := []string{"busybox:1.36", "nginx:1.27"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerImages = %v, want %v", got, want)
	}

	if got := containerImages(mkObj(t, nil, nil), "spec"); got != nil {
		t.Errorf("no containers should mean nil, got %v", got)
	}
}
