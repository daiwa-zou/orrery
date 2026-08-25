package api

import "testing"

func TestRevisionOf(t *testing.T) {
	rs := mkObj(t, map[string]any{
		"annotations": map[string]any{revisionAnnotation: "7"},
	}, nil)
	if got := revisionOf(rs); got != 7 {
		t.Errorf("revisionOf = %d, want 7", got)
	}

	// A ReplicaSet not managed by a Deployment has no revision annotation;
	// zero sorts it last, which is exactly where it belongs in history.
	if got := revisionOf(mkObj(t, nil, nil)); got != 0 {
		t.Errorf("unannotated revision = %d, want 0", got)
	}
	garbage := mkObj(t, map[string]any{
		"annotations": map[string]any{revisionAnnotation: "not-a-number"},
	}, nil)
	if got := revisionOf(garbage); got != 0 {
		t.Errorf("unparseable revision = %d, want 0", got)
	}
}
