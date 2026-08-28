package api

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// What a rollback would actually change.
//
// `kubectl rollout history` lists revisions by number, and a console that does
// the same leaves the reader with the one question the list was opened to
// answer — which of these do I go back to? — and nothing to answer it with.
// Two revisions of the same deployment routinely carry the same image: a
// config change, a resource bump, an added environment variable. Side by side
// they are indistinguishable, and the difference is precisely what is being
// chosen between.
//
// So each revision is compared against the one deployed now, and reports the
// difference in the terms the choice is made in. Images first and by container,
// because that is what people look for; then the other parts of the pod
// template that differ, named rather than diffed — "env", "resources" — since a
// full structural diff in a table cell is unreadable and the YAML tab is one
// click away for anyone who wants it.
//
// The load-bearing case is the empty one. A revision whose template matches the
// current one exactly would roll back to no change at all, and there is nothing
// on screen to say so: same images, same age, same everything, and a button
// that appears to offer something. Saying "identical" is the difference between
// a decision and a coin toss.

// podTemplateOf returns a revision's pod template, or nil when it has none.
func podTemplateOf(rs *unstructured.Unstructured) map[string]any {
	if rs == nil {
		return nil
	}
	raw, found, err := unstructured.NestedMap(rs.Object, "spec", "template")
	if !found || err != nil {
		return nil
	}
	return raw
}

// containersByName indexes a template's containers, init containers included,
// so a change can be attributed to the container it happened in.
func containersByName(template map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	spec, _ := template["spec"].(map[string]any)
	for _, field := range []string{"initContainers", "containers"} {
		list, _ := spec[field].([]any)
		for _, c := range list {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := m["name"].(string); name != "" {
				out[name] = m
			}
		}
	}
	return out
}

// imageChanges reports the image moves a rollback would make, phrased in the
// direction it would travel: what is running now, then what this revision
// would put back.
func imageChanges(current, target map[string]any) []string {
	now := containersByName(current)
	then := containersByName(target)

	names := make([]string, 0, len(now)+len(then))
	seen := map[string]bool{}
	for _, m := range []map[string]map[string]any{now, then} {
		for name := range m {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)

	var out []string
	for _, name := range names {
		nowImage, _ := now[name]["image"].(string)
		thenImage, _ := then[name]["image"].(string)
		switch {
		case nowImage == thenImage:
			continue
		case nowImage == "":
			out = append(out, fmt.Sprintf("adds container %s (%s)", name, thenImage))
		case thenImage == "":
			out = append(out, fmt.Sprintf("removes container %s", name))
		default:
			out = append(out, fmt.Sprintf("%s: %s → %s", name, nowImage, thenImage))
		}
	}
	return out
}

// containerFields are the parts of a container worth naming separately. A
// reader chooses a rollback target by these; everything else about a container
// is reported as "other".
var containerFields = []string{
	"command", "args", "env", "envFrom", "resources", "ports",
	"volumeMounts", "livenessProbe", "readinessProbe", "startupProbe",
	"securityContext", "imagePullPolicy",
}

// podSpecFields are the same, one level up.
var podSpecFields = []string{
	"serviceAccountName", "nodeSelector", "affinity", "tolerations", "volumes",
	"securityContext", "priorityClassName", "hostNetwork", "dnsPolicy",
	"terminationGracePeriodSeconds", "imagePullSecrets", "initContainers",
}

// podTemplateHash is written onto every revision's template by the Deployment
// controller, derived from the template itself. It therefore differs between
// any two revisions by construction, and reporting it would put "template
// labels" on every row of the history while meaning nothing anyone changed.
const podTemplateHash = "pod-template-hash"

// withoutHash copies a label map without the controller's own hash.
func withoutHash(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	if _, ok := m[podTemplateHash]; !ok {
		return m
	}
	out := make(map[string]any, len(m)-1)
	for k, v := range m {
		if k != podTemplateHash {
			out[k] = v
		}
	}
	return out
}

// withoutTemplateHash copies a pod template with the controller's hash label
// removed, one level deep — enough, since only metadata.labels is rewritten.
func withoutTemplateHash(template map[string]any) map[string]any {
	meta, _ := template["metadata"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	if _, ok := labels[podTemplateHash]; !ok {
		return template
	}
	outMeta := make(map[string]any, len(meta))
	for k, v := range meta {
		outMeta[k] = v
	}
	outMeta["labels"] = withoutHash(labels)

	out := make(map[string]any, len(template))
	for k, v := range template {
		out[k] = v
	}
	out["metadata"] = outMeta
	return out
}

// otherChanges names the fields — beyond images — where the two templates
// disagree. Names rather than values: "env" is what someone needs to know to
// decide whether to look, and a rendered diff of an env block does not fit in
// a table.
func otherChanges(current, target map[string]any) []string {
	var out []string
	add := func(s string) {
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}

	nowMeta, _ := current["metadata"].(map[string]any)
	thenMeta, _ := target["metadata"].(map[string]any)
	for _, field := range []string{"labels", "annotations"} {
		if !reflect.DeepEqual(withoutHash(nowMeta[field]), withoutHash(thenMeta[field])) {
			add("template " + field)
		}
	}

	nowSpec, _ := current["spec"].(map[string]any)
	thenSpec, _ := target["spec"].(map[string]any)
	for _, field := range podSpecFields {
		if !reflect.DeepEqual(nowSpec[field], thenSpec[field]) {
			add(field)
		}
	}

	now := containersByName(current)
	then := containersByName(target)
	for name, c := range now {
		other, ok := then[name]
		if !ok {
			continue
		}
		for _, field := range containerFields {
			if !reflect.DeepEqual(c[field], other[field]) {
				add(field)
			}
		}
	}

	sort.Strings(out)
	return out
}

// revisionChanges summarises what rolling back to one revision would change,
// and whether it would change anything at all.
//
// The boolean is not derivable from an empty list: a difference this function
// does not name — an unrecognised field, a reordered container list — still
// makes the two templates different, and reporting "identical" then would be
// a claim about the cluster that is not true. Empty changes with identical
// false means "different, but not in the parts named here".
func revisionChanges(current, target *unstructured.Unstructured) (changes []string, identical bool) {
	nowTemplate := podTemplateOf(current)
	thenTemplate := podTemplateOf(target)
	if nowTemplate == nil || thenTemplate == nil {
		return nil, false
	}
	// Compared without the controller's hash. It is derived from the rest of
	// the template, so leaving it in would make "identical" unreachable — every
	// revision is a different ReplicaSet and carries a different hash — while
	// answering a question nobody asked. Two templates equal apart from it
	// would deploy the same pods, which is what the reader is being told.
	if reflect.DeepEqual(withoutTemplateHash(nowTemplate), withoutTemplateHash(thenTemplate)) {
		return nil, true
	}
	changes = append(changes, imageChanges(nowTemplate, thenTemplate)...)
	changes = append(changes, otherChanges(nowTemplate, thenTemplate)...)
	return changes, false
}

// summarizeChanges renders the changes for a table cell, keeping the count
// honest when there are more than fit.
func summarizeChanges(changes []string, max int) string {
	if len(changes) <= max {
		return strings.Join(changes, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(changes[:max], ", "), len(changes)-max)
}
