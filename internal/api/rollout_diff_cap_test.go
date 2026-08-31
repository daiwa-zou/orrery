package api

// maxDiffLines caps what a revision's diff returns; nothing capped how many
// diffs were run. The list is every ReplicaSet the deployment owns — which is
// revisionHistoryLimit, and therefore whatever an operator set it to — and each
// entry costs a longest-common-subsequence table over two pod templates. That
// table is quadratic: measured at the 2000-line input cap it is 4.6ms and
// 16.6MB, so a deployment keeping two hundred revisions of a large template is
// most of a second of CPU and gigabytes of allocation churn, for one GET that
// any viewer of the deployment may make.
//
// The bound must not cost the answer its honesty. A revision past the ceiling
// still carries its named Changes and its Identical verdict; only the
// line-by-line detail stops. Saying nothing would leave it with no Diff and no
// reason, which is precisely what Identical looks like — and "rolling back here
// would change nothing" is the one wrong answer available to this page.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type revisionsBody struct {
	Revisions []struct {
		Revision    int64      `json:"revision"`
		Name        string     `json:"name"`
		Current     bool       `json:"current"`
		Changes     []string   `json:"changes"`
		Diff        []diffLine `json:"diff"`
		Identical   bool       `json:"identical"`
		DiffOmitted bool       `json:"diffOmitted"`
	} `json:"revisions"`
}

// seedRevisions adds n distinct ReplicaSet revisions to the fake, each with a
// template that genuinely differs from the deployed one.
func seedRevisions(rig *hndRig, n int) {
	owner := []any{map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "uid-web",
	}}
	rig.fake.set(func(f *hndFake) {
		rs := f.resources[hndKey("apps", "v1", "replicasets")]
		for i := range n {
			rev := 100 + i
			name := fmt.Sprintf("web-rev%d", rev)
			rs.items = append(rs.items, hndObj("apps", "v1", "ReplicaSet", "demo", name, map[string]any{
				"annotations":     map[string]any{"deployment.kubernetes.io/revision": fmt.Sprint(rev)},
				"ownerReferences": owner,
				"spec": map[string]any{"replicas": int64(0), "template": map[string]any{
					"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
					"spec": map[string]any{"containers": []any{
						map[string]any{"name": "app", "image": fmt.Sprintf("web:%d", rev)},
					}},
				}},
				"status": map[string]any{"replicas": int64(0)},
			}))
		}
	})
}

func TestRolloutHistoryBoundsHowManyDiffsItComputes(t *testing.T) {
	rig := hndNewRig(t)
	seedRevisions(rig, maxDiffedRevisions+5)

	rec := rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=web")
	hndWantStatus(t, rec, http.StatusOK)

	var body revisionsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	withDiff, omitted := 0, 0
	for _, r := range body.Revisions {
		if r.Current {
			continue
		}
		switch {
		case len(r.Diff) > 0:
			withDiff++
		case r.DiffOmitted:
			omitted++
		}
		// Whichever side of the ceiling it fell on, a revision that differs
		// must never come back claiming it does not.
		if r.DiffOmitted && r.Identical {
			t.Errorf("revision %d was skipped and reported identical", r.Revision)
		}
	}

	if withDiff > maxDiffedRevisions {
		t.Errorf("computed %d diffs, want at most %d", withDiff, maxDiffedRevisions)
	}
	if omitted == 0 {
		t.Errorf("seeded %d revisions past the ceiling of %d and none was marked omitted",
			maxDiffedRevisions+5, maxDiffedRevisions)
	}
}

// Every revision is still listed and still summarised — the ceiling bounds the
// expensive detail, not the answer.
func TestRolloutHistoryStillListsEveryRevision(t *testing.T) {
	rig := hndNewRig(t)
	const extra = 5
	seedRevisions(rig, maxDiffedRevisions+extra)

	rec := rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=web")
	hndWantStatus(t, rec, http.StatusOK)

	var body revisionsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Two revisions come with the fixture.
	if want := maxDiffedRevisions + extra + 2; len(body.Revisions) != want {
		t.Fatalf("listed %d revisions, want %d", len(body.Revisions), want)
	}
	for _, r := range body.Revisions {
		if r.Current {
			continue
		}
		if len(r.Changes) == 0 && !r.Identical {
			t.Errorf("revision %d (%s) carries neither changes nor an identical verdict",
				r.Revision, r.Name)
		}
	}
}

// A history short enough to diff in full says nothing about a ceiling.
func TestRolloutHistoryUnderTheCeilingMarksNothingOmitted(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=web")
	hndWantStatus(t, rec, http.StatusOK)

	var body revisionsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range body.Revisions {
		if r.DiffOmitted {
			t.Errorf("revision %d was marked omitted in a two-revision history", r.Revision)
		}
	}
}
