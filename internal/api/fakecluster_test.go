package api

// This file is the end-to-end test rig: an in-memory Kubernetes API server
// good enough for discovery, the dynamic and typed clients, access reviews,
// metrics and OpenAPI, plus a builder that wires a real Registry, auth
// middleware (anonymous mode) and the chi router on top of it. Handler tests
// drive the router exactly the way the browser does, so the authorization
// walk (cache-then-SubjectAccessReview) is exercised for real, never bypassed.
//
// Every helper here is prefixed "hnd" to stay clear of helpers in the other
// test files of this package.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	clientscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/daiwa-zou/orrery/internal/auth"
	"github.com/daiwa-zou/orrery/internal/cluster"
	"github.com/daiwa-zou/orrery/internal/config"
)

// hndResource is one served resource: its kind, scope and current objects.
type hndResource struct {
	kind       string
	namespaced bool
	items      []map[string]any
}

// hndFake is the in-memory API server. All state is guarded by mu because the
// authz layer fires concurrent reviews and informers list in the background.
type hndFake struct {
	mu        sync.Mutex
	resources map[string]*hndResource // "group|version|resource"
	discovery map[string]any          // "/api/v1" etc. -> APIResourceList
	groups    any                     // /apis payload
	evicted   []string                // "ns/name" of every eviction POST
	// ephemeral records every ephemeral container added, as
	// "ns/pod/name:image:target", so a test can assert what was actually sent
	// rather than only that the call returned 200.
	ephemeral []string
	logText   string
	// denyResource makes access reviews for one resource come back denied, so
	// tests can walk the forbidden path end to end.
	denyResource string
	// nsOnlyResource denies only the cluster-wide review for one resource,
	// which forces the per-namespace fallback scan.
	nsOnlyResource string
	// failReviewResource makes access reviews for one resource fail outright
	// rather than come back denied — the difference between "you may not" and
	// "we could not ask", which callers must not collapse.
	failReviewResource string
	// trace, when set, records every path the fake is asked for, so a test can
	// assert on where a request actually landed rather than only on its status.
	trace func(string)
	// hideResource drops one resource from the discovery document, so a
	// lookup for it fails the way it would on a cluster that does not serve
	// it — or on one whose discovery is not answering.
	hideResource string
	// breakCacheResource fails the informer's watch for one resource while
	// discovery keeps advertising it, which is what an unsynced cache looks
	// like from the API's side: the resource resolves, and then reading it
	// returns an error rather than an empty list.
	breakCacheResource string
}

func hndKey(group, version, resource string) string {
	return group + "|" + version + "|" + resource
}

func hndAPIVersion(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}

func hndWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func hndStatus(w http.ResponseWriter, code int, reason, msg string) {
	hndWriteJSON(w, code, map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "message": msg, "code": code,
	})
}

// hndObj builds one stored object.
func hndObj(group, version, kind, ns, name string, extra map[string]any) map[string]any {
	meta := map[string]any{
		"name":              name,
		"uid":               "uid-" + name,
		"creationTimestamp": "2024-01-01T00:00:00Z",
	}
	if ns != "" {
		meta["namespace"] = ns
	}
	o := map[string]any{
		"apiVersion": hndAPIVersion(group, version),
		"kind":       kind,
		"metadata":   meta,
	}
	for k, v := range extra {
		if k == "labels" || k == "annotations" || k == "ownerReferences" {
			meta[k] = v
			continue
		}
		o[k] = v
	}
	return o
}

func hndAPIResource(name, singular, kind string, namespaced bool, verbs []string) map[string]any {
	return map[string]any{
		"name": name, "singularName": singular, "kind": kind,
		"namespaced": namespaced, "verbs": verbs,
	}
}

var hndAllVerbs = []string{"create", "delete", "get", "list", "patch", "update", "watch"}
var hndReadVerbs = []string{"get", "list", "watch"}

func hndNewFake() *hndFake {
	f := &hndFake{
		resources: map[string]*hndResource{},
		logText:   "line-1\nline-2\n",
	}

	list := func(path, groupVersion string, resources ...map[string]any) {
		f.discovery[path] = map[string]any{
			"kind": "APIResourceList", "apiVersion": "v1",
			"groupVersion": groupVersion, "resources": resources,
		}
	}
	f.discovery = map[string]any{}
	list("/api/v1", "v1",
		hndAPIResource("pods", "pod", "Pod", true, hndAllVerbs),
		hndAPIResource("nodes", "node", "Node", false, []string{"get", "list", "patch", "watch"}),
		hndAPIResource("services", "service", "Service", true, hndAllVerbs),
		hndAPIResource("events", "event", "Event", true, hndReadVerbs),
		hndAPIResource("namespaces", "namespace", "Namespace", false, hndReadVerbs),
		hndAPIResource("configmaps", "configmap", "ConfigMap", true, hndAllVerbs),
		hndAPIResource("secrets", "secret", "Secret", true, hndReadVerbs),
		hndAPIResource("persistentvolumeclaims", "persistentvolumeclaim", "PersistentVolumeClaim", true, hndReadVerbs),
		hndAPIResource("persistentvolumes", "persistentvolume", "PersistentVolume", false, hndReadVerbs),
	)
	list("/apis/apps/v1", "apps/v1",
		hndAPIResource("deployments", "deployment", "Deployment", true, hndAllVerbs),
		hndAPIResource("replicasets", "replicaset", "ReplicaSet", true, hndReadVerbs),
		hndAPIResource("statefulsets", "statefulset", "StatefulSet", true, hndAllVerbs),
		hndAPIResource("daemonsets", "daemonset", "DaemonSet", true, hndAllVerbs),
	)
	list("/apis/batch/v1", "batch/v1",
		hndAPIResource("jobs", "job", "Job", true, hndAllVerbs),
		hndAPIResource("cronjobs", "cronjob", "CronJob", true, hndAllVerbs),
	)
	list("/apis/networking.k8s.io/v1", "networking.k8s.io/v1",
		hndAPIResource("ingresses", "ingress", "Ingress", true, hndReadVerbs),
	)
	list("/apis/authorization.k8s.io/v1", "authorization.k8s.io/v1",
		hndAPIResource("subjectaccessreviews", "subjectaccessreview", "SubjectAccessReview", false, []string{"create"}),
		hndAPIResource("selfsubjectaccessreviews", "selfsubjectaccessreview", "SelfSubjectAccessReview", false, []string{"create"}),
	)
	list("/apis/apiextensions.k8s.io/v1", "apiextensions.k8s.io/v1",
		hndAPIResource("customresourcedefinitions", "customresourcedefinition", "CustomResourceDefinition", false, hndAllVerbs),
	)
	list("/apis/example.com/v1", "example.com/v1",
		hndAPIResource("widgets", "widget", "Widget", true, hndReadVerbs),
	)
	list("/apis/metrics.k8s.io/v1beta1", "metrics.k8s.io/v1beta1",
		hndAPIResource("nodes", "", "NodeMetrics", false, []string{"get", "list"}),
		hndAPIResource("pods", "", "PodMetrics", true, []string{"get", "list"}),
	)

	group := func(name, version string) map[string]any {
		gv := name + "/" + version
		return map[string]any{
			"name":             name,
			"versions":         []map[string]any{{"groupVersion": gv, "version": version}},
			"preferredVersion": map[string]any{"groupVersion": gv, "version": version},
		}
	}
	f.groups = map[string]any{
		"kind": "APIGroupList", "apiVersion": "v1",
		"groups": []map[string]any{
			group("apps", "v1"),
			group("batch", "v1"),
			group("networking.k8s.io", "v1"),
			group("authorization.k8s.io", "v1"),
			group("apiextensions.k8s.io", "v1"),
			group("example.com", "v1"),
			group("metrics.k8s.io", "v1beta1"),
		},
	}

	f.seed()
	return f
}

// seed loads the fixture cluster: one node, a handful of pods in "demo", a
// deployment with two revisions, a daemonset, a cronjob and two events.
func (f *hndFake) seed() {
	add := func(group, version, resource, kind string, namespaced bool, items ...map[string]any) {
		f.resources[hndKey(group, version, resource)] = &hndResource{kind: kind, namespaced: namespaced, items: items}
	}

	add("", "v1", "namespaces", "Namespace", false,
		hndObj("", "v1", "Namespace", "", "demo", nil),
		hndObj("", "v1", "Namespace", "", "kube-system", nil),
	)
	add("", "v1", "nodes", "Node", false,
		hndObj("", "v1", "Node", "", "node-1", map[string]any{
			"spec": map[string]any{},
			"status": map[string]any{
				"allocatable": map[string]any{"cpu": "2", "memory": "4Gi"},
				"capacity":    map[string]any{"cpu": "2", "memory": "4Gi"},
				"conditions":  []any{map[string]any{"type": "Ready", "status": "True"}},
			},
		}),
	)

	podSpec := func(node, image string) map[string]any {
		return map[string]any{
			"nodeName": node,
			"containers": []any{map[string]any{
				"name": "app", "image": image,
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "100m", "memory": "64Mi"},
					"limits":   map[string]any{"cpu": "200m", "memory": "128Mi"},
				},
			}},
		}
	}
	running := map[string]any{
		"phase": "Running",
		"containerStatuses": []any{map[string]any{
			"name": "app", "ready": true, "restartCount": int64(0),
			"state": map[string]any{"running": map[string]any{}},
		}},
	}
	add("", "v1", "pods", "Pod", true,
		hndObj("", "v1", "Pod", "demo", "web-1", map[string]any{
			"labels": map[string]any{"app": "web"},
			"spec":   podSpec("node-1", "web:2"),
			"status": running,
		}),
		hndObj("", "v1", "Pod", "demo", "web-2", map[string]any{
			"labels": map[string]any{"app": "web"},
			"spec":   podSpec("node-1", "web:2"),
			"status": running,
		}),
		hndObj("", "v1", "Pod", "demo", "done-1", map[string]any{
			"spec":   podSpec("node-1", "job:1"),
			"status": map[string]any{"phase": "Succeeded"},
		}),
		hndObj("", "v1", "Pod", "demo", "ds-1", map[string]any{
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "DaemonSet", "name": "ds", "uid": "uid-ds",
			}},
			"spec":   podSpec("node-1", "agent:1"),
			"status": running,
		}),
	)
	add("", "v1", "services", "Service", true,
		hndObj("", "v1", "Service", "demo", "svc", map[string]any{
			"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.0.0.1"},
		}),
	)
	add("", "v1", "configmaps", "ConfigMap", true)
	add("", "v1", "secrets", "Secret", true)
	// A bound claim and its volume, so the reference edge between them has
	// something real to walk.
	add("", "v1", "persistentvolumeclaims", "PersistentVolumeClaim", true,
		hndObj("", "v1", "PersistentVolumeClaim", "demo", "data", map[string]any{
			"spec":   map[string]any{"volumeName": "pv-data"},
			"status": map[string]any{"phase": "Bound"},
		}),
	)
	add("", "v1", "persistentvolumes", "PersistentVolume", false,
		hndObj("", "v1", "PersistentVolume", "", "pv-data", map[string]any{
			"spec":   map[string]any{"capacity": map[string]any{"storage": "1Gi"}},
			"status": map[string]any{"phase": "Bound"},
		}),
	)
	add("", "v1", "events", "Event", true,
		hndObj("", "v1", "Event", "demo", "ev-1", map[string]any{
			"type": "Normal", "reason": "Started", "message": "Started container app",
			"involvedObject": map[string]any{"kind": "Pod", "name": "web-1", "namespace": "demo", "uid": "uid-web-1"},
			"lastTimestamp":  "2024-01-02T10:00:00Z",
			"count":          int64(1),
		}),
		hndObj("", "v1", "Event", "demo", "ev-2", map[string]any{
			"type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container",
			"involvedObject": map[string]any{"kind": "Pod", "name": "web-2", "namespace": "demo", "uid": "uid-web-2"},
			"lastTimestamp":  "2024-01-02T11:00:00Z",
			"count":          int64(3),
		}),
	)

	template := func(image string, hash bool) map[string]any {
		labels := map[string]any{"app": "web"}
		if hash {
			labels["pod-template-hash"] = "abc123"
		}
		return map[string]any{
			"metadata": map[string]any{"labels": labels},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": image}}},
		}
	}
	add("apps", "v1", "deployments", "Deployment", true,
		hndObj("apps", "v1", "Deployment", "demo", "web", map[string]any{
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "2"},
			"spec":        map[string]any{"replicas": int64(2), "template": template("web:2", false)},
			"status":      map[string]any{"replicas": int64(2), "readyReplicas": int64(2)},
		}),
	)
	owner := []any{map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "uid-web",
	}}
	add("apps", "v1", "replicasets", "ReplicaSet", true,
		hndObj("apps", "v1", "ReplicaSet", "demo", "web-rev2", map[string]any{
			"annotations":     map[string]any{"deployment.kubernetes.io/revision": "2"},
			"ownerReferences": owner,
			"spec":            map[string]any{"replicas": int64(2), "template": template("web:2", true)},
			"status":          map[string]any{"replicas": int64(2)},
		}),
		hndObj("apps", "v1", "ReplicaSet", "demo", "web-rev1", map[string]any{
			"annotations":     map[string]any{"deployment.kubernetes.io/revision": "1", "kubernetes.io/change-cause": "initial rollout"},
			"ownerReferences": owner,
			"spec":            map[string]any{"replicas": int64(0), "template": template("web:1", true)},
			"status":          map[string]any{"replicas": int64(0)},
		}),
	)
	add("apps", "v1", "statefulsets", "StatefulSet", true)
	add("apps", "v1", "daemonsets", "DaemonSet", true,
		hndObj("apps", "v1", "DaemonSet", "demo", "ds", map[string]any{
			"spec":   map[string]any{},
			"status": map[string]any{"desiredNumberScheduled": int64(1), "numberReady": int64(1)},
		}),
	)
	add("batch", "v1", "jobs", "Job", true)
	add("batch", "v1", "cronjobs", "CronJob", true,
		hndObj("batch", "v1", "CronJob", "demo", "cj", map[string]any{
			"spec": map[string]any{
				"schedule": "0 0 * * *",
				"suspend":  false,
				"jobTemplate": map[string]any{
					"metadata": map[string]any{"labels": map[string]any{"app": "cj"}},
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"restartPolicy": "Never",
								"containers":    []any{map[string]any{"name": "c", "image": "busybox"}},
							},
						},
					},
				},
			},
		}),
	)
	add("networking.k8s.io", "v1", "ingresses", "Ingress", true,
		hndObj("networking.k8s.io", "v1", "Ingress", "demo", "web-ing", map[string]any{
			"spec": map[string]any{
				"defaultBackend": map[string]any{
					"service": map[string]any{"name": "svc", "port": map[string]any{"number": int64(80)}},
				},
				"rules": []any{map[string]any{
					"host": "web.example",
					"http": map[string]any{"paths": []any{map[string]any{
						"path": "/",
						"backend": map[string]any{
							"service": map[string]any{"name": "web-backend", "port": map[string]any{"number": int64(8080)}},
						},
					}}},
				}},
			},
		}),
	)

	// A CRD with printer columns plus two of its objects, so the table path
	// that compiles additionalPrinterColumns runs against real discovery.
	add("apiextensions.k8s.io", "v1", "customresourcedefinitions", "CustomResourceDefinition", false,
		hndObj("apiextensions.k8s.io", "v1", "CustomResourceDefinition", "", "widgets.example.com", map[string]any{
			"spec": map[string]any{
				"group": "example.com",
				"versions": []any{map[string]any{
					"name": "v1",
					"additionalPrinterColumns": []any{
						map[string]any{"name": "Color", "type": "string", "jsonPath": ".spec.color"},
						map[string]any{"name": "Count", "type": "integer", "jsonPath": ".spec.count"},
					},
				}},
			},
		}),
	)
	add("example.com", "v1", "widgets", "Widget", true,
		hndObj("example.com", "v1", "Widget", "demo", "w-1", map[string]any{
			"spec": map[string]any{"color": "red", "count": int64(2)},
		}),
		hndObj("example.com", "v1", "Widget", "demo", "w-2", map[string]any{
			"spec": map[string]any{"color": "blue", "count": int64(7)},
		}),
	)

	add("metrics.k8s.io", "v1beta1", "nodes", "NodeMetrics", false,
		map[string]any{
			"metadata":  map[string]any{"name": "node-1"},
			"timestamp": "2024-01-02T12:00:00Z", "window": "10s",
			"usage": map[string]any{"cpu": "500m", "memory": "1024Mi"},
		},
	)
	add("metrics.k8s.io", "v1beta1", "pods", "PodMetrics", true,
		map[string]any{
			"metadata":  map[string]any{"name": "web-1", "namespace": "demo"},
			"timestamp": "2024-01-02T12:00:00Z", "window": "10s",
			"containers": []any{map[string]any{
				"name": "app", "usage": map[string]any{"cpu": "250m", "memory": "128Mi"},
			}},
		},
		map[string]any{
			"metadata":  map[string]any{"name": "web-2", "namespace": "demo"},
			"timestamp": "2024-01-02T12:00:00Z", "window": "10s",
			"containers": []any{map[string]any{
				"name": "app", "usage": map[string]any{"cpu": "100m", "memory": "64Mi"},
			}},
		},
	)
}

// get returns a stored object, or nil. Caller must hold f.mu.
func (f *hndFake) getLocked(key, ns, name string) (int, map[string]any) {
	res := f.resources[key]
	if res == nil {
		return -1, nil
	}
	for i, o := range res.items {
		meta, _ := o["metadata"].(map[string]any)
		oname, _ := meta["name"].(string)
		ons, _ := meta["namespace"].(string)
		if oname == name && (!res.namespaced || ons == ns) {
			return i, o
		}
	}
	return -1, nil
}

// object looks one stored object up for test assertions.
func (f *hndFake) object(group, version, resource, ns, name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, o := f.getLocked(hndKey(group, version, resource), ns, name)
	return o
}

func (f *hndFake) ephemeralContainers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ephemeral...)
}

func (f *hndFake) evictions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.evicted...)
}

func (f *hndFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if fn := f.tracer(); fn != nil {
		fn(r.URL.Path)
	}

	switch p {
	case "/version":
		hndWriteJSON(w, 200, map[string]any{"major": "1", "minor": "30", "gitVersion": "v1.30.0"})
		return
	case "/readyz":
		_, _ = w.Write([]byte("ok"))
		return
	case "/api":
		hndWriteJSON(w, 200, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		return
	case "/apis":
		hndWriteJSON(w, 200, f.groups)
		return
	case "/openapi/v3":
		hndWriteJSON(w, 200, map[string]any{
			"paths": map[string]any{
				"api/v1": map[string]any{"serverRelativeURL": "/openapi/v3/api/v1"},
			},
		})
		return
	case "/openapi/v3/api/v1":
		hndWriteJSON(w, 200, hndOpenAPIDoc())
		return
	case "/apis/authorization.k8s.io/v1/subjectaccessreviews",
		"/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
		f.serveAccessReview(w, r, p)
		return
	}

	if doc, ok := f.discovery[p]; ok {
		hndWriteJSON(w, 200, f.filterDiscovery(doc))
		return
	}

	var group, version, rest string
	switch {
	case strings.HasPrefix(p, "/api/v1/"):
		group, version, rest = "", "v1", strings.TrimPrefix(p, "/api/v1/")
	case strings.HasPrefix(p, "/apis/"):
		segs := strings.SplitN(strings.TrimPrefix(p, "/apis/"), "/", 3)
		if len(segs) < 3 {
			hndStatus(w, 404, "NotFound", "no such path "+p)
			return
		}
		group, version, rest = segs[0], segs[1], segs[2]
	default:
		hndStatus(w, 404, "NotFound", "no such path "+p)
		return
	}
	f.serveResource(w, r, group, version, rest)
}

// setTrace installs the path recorder. Informer goroutines are already serving
// from this fake by the time a test sets one, so the field is guarded like
// every other piece of mutable state here.
func (f *hndFake) setTrace(fn func(string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace = fn
}

func (f *hndFake) tracer() func(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trace
}

// filterDiscovery applies hideResource to an APIResourceList.
func (f *hndFake) filterDiscovery(doc any) any {
	f.mu.Lock()
	hide := f.hideResource
	f.mu.Unlock()
	if hide == "" {
		return doc
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return doc
	}
	list, ok := m["resources"].([]map[string]any)
	if !ok {
		return doc
	}
	kept := make([]map[string]any, 0, len(list))
	for _, r := range list {
		if name, _ := r["name"].(string); name == hide {
			continue
		}
		kept = append(kept, r)
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	out["resources"] = kept
	return out
}

// hndProto decodes the "k8s" protobuf envelope typed clients send by default.
var hndProto = protobuf.NewSerializer(clientscheme.Scheme, clientscheme.Scheme)

// hndDecodePod reads a Pod from a request body in whichever encoding the
// client chose.
func hndDecodePod(r *http.Request, body []byte) (*corev1.Pod, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/vnd.kubernetes.protobuf") {
		obj, _, err := hndProto.Decode(body, nil, &corev1.Pod{})
		if err != nil {
			return nil, err
		}
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return nil, fmt.Errorf("decoded %T, not a Pod", obj)
		}
		return pod, nil
	}
	var pod corev1.Pod
	if err := json.Unmarshal(body, &pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

// serveAccessReview grants everything except the configured denied resource.
// Never returning an error keeps the authz layer on its happy path; denials
// still flow through the real SubjectAccessReview verdict.
func (f *hndFake) serveAccessReview(w http.ResponseWriter, r *http.Request, path string) {
	body, _ := io.ReadAll(r.Body)

	var attrs *authzv1.ResourceAttributes
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/vnd.kubernetes.protobuf") {
		if obj, _, err := hndProto.Decode(body, nil, nil); err == nil {
			switch rv := obj.(type) {
			case *authzv1.SubjectAccessReview:
				attrs = rv.Spec.ResourceAttributes
			case *authzv1.SelfSubjectAccessReview:
				attrs = rv.Spec.ResourceAttributes
			}
		}
	} else {
		// Both review kinds share the resourceAttributes shape in JSON.
		var sar authzv1.SubjectAccessReview
		if json.Unmarshal(body, &sar) == nil {
			attrs = sar.Spec.ResourceAttributes
		}
	}

	allowed := true
	if attrs != nil {
		f.mu.Lock()
		deny, nsOnly := f.denyResource, f.nsOnlyResource
		failReview := f.failReviewResource
		f.mu.Unlock()
		if failReview != "" && attrs.Resource == failReview {
			hndStatus(w, 500, "InternalError", "the access review could not be performed")
			return
		}
		if deny != "" && attrs.Resource == deny {
			allowed = false
		}
		if nsOnly != "" && attrs.Resource == nsOnly && attrs.Namespace == "" {
			allowed = false
		}
	}

	kind := "SubjectAccessReview"
	if strings.Contains(path, "selfsubject") {
		kind = "SelfSubjectAccessReview"
	}
	hndWriteJSON(w, 201, map[string]any{
		"kind": kind, "apiVersion": "authorization.k8s.io/v1",
		"status": map[string]any{"allowed": allowed},
	})
}

func (f *hndFake) serveResource(w http.ResponseWriter, r *http.Request, group, version, rest string) {
	segs := strings.Split(rest, "/")
	var ns, resource, name, sub string
	if segs[0] == "namespaces" && len(segs) >= 3 {
		ns, resource = segs[1], segs[2]
		if len(segs) >= 4 {
			name = segs[3]
		}
		if len(segs) >= 5 {
			sub = strings.Join(segs[4:], "/")
		}
	} else {
		resource = segs[0]
		if len(segs) >= 2 {
			name = segs[1]
		}
		if len(segs) >= 3 {
			sub = strings.Join(segs[2:], "/")
		}
	}

	// Subresources first: they do not follow the storage shape.
	switch {
	case sub == "log":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(f.logText))
		return
	case sub == "eviction":
		f.mu.Lock()
		f.evicted = append(f.evicted, ns+"/"+name)
		f.mu.Unlock()
		hndWriteJSON(w, 201, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})
		return
	case sub == "ephemeralcontainers":
		// UpdateEphemeralContainers PUTs the whole pod back. The typed client
		// speaks protobuf by default, so this decodes the same envelope the
		// access-review handler does; the reply goes back as JSON, which
		// client-go accepts whichever it asked for.
		body, _ := io.ReadAll(r.Body)
		pod, err := hndDecodePod(r, body)
		if err != nil {
			hndStatus(w, 400, "BadRequest", "undecodable pod: "+err.Error())
			return
		}
		f.mu.Lock()
		for _, c := range pod.Spec.EphemeralContainers {
			f.ephemeral = append(f.ephemeral,
				ns+"/"+name+"/"+c.Name+":"+c.Image+":"+c.TargetContainerName)
		}
		f.mu.Unlock()
		hndWriteJSON(w, 200, pod)
		return
	case strings.HasPrefix(sub, "proxy"):
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("hello-from-proxy"))
		return
	}

	key := hndKey(group, version, resource)

	// Watches: client-go v0.36 informers use the WatchList protocol — a watch
	// with sendInitialEvents=true that must deliver ADDED events followed by a
	// bookmark annotated "initial-events-end" before the cache counts as
	// synced. After the initial burst the stream hangs until the client goes.
	if r.URL.Query().Get("watch") == "true" {
		f.mu.Lock()
		broken := f.breakCacheResource
		f.mu.Unlock()
		if broken != "" && resource == broken {
			hndStatus(w, 500, "InternalError", "the watch for "+resource+" is broken")
			return
		}

		f.mu.Lock()
		res := f.resources[key]
		if res == nil {
			f.mu.Unlock()
			hndStatus(w, 404, "NotFound", "the server could not find the requested resource "+key)
			return
		}
		kind := res.kind
		var frames [][]byte
		if r.URL.Query().Get("sendInitialEvents") == "true" {
			for _, o := range res.items {
				meta, _ := o["metadata"].(map[string]any)
				ons, _ := meta["namespace"].(string)
				if ns != "" && res.namespaced && ons != ns {
					continue
				}
				ev, _ := json.Marshal(map[string]any{"type": "ADDED", "object": o})
				frames = append(frames, ev)
			}
			bookmark, _ := json.Marshal(map[string]any{
				"type": "BOOKMARK",
				"object": map[string]any{
					"kind": kind, "apiVersion": hndAPIVersion(group, version),
					"metadata": map[string]any{
						"resourceVersion": "1",
						"annotations":     map[string]any{"k8s.io/initial-events-end": "true"},
					},
				},
			})
			frames = append(frames, bookmark)
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		for _, fr := range frames {
			_, _ = w.Write(fr)
			_, _ = w.Write([]byte("\n"))
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	res := f.resources[key]
	if res == nil {
		hndStatus(w, 404, "NotFound", "the server could not find the requested resource "+key)
		return
	}

	switch {
	case r.Method == http.MethodGet && name == "":
		items := make([]map[string]any, 0, len(res.items))
		for _, o := range res.items {
			meta, _ := o["metadata"].(map[string]any)
			ons, _ := meta["namespace"].(string)
			if ns != "" && res.namespaced && ons != ns {
				continue
			}
			items = append(items, o)
		}
		hndWriteJSON(w, 200, map[string]any{
			"kind": res.kind + "List", "apiVersion": hndAPIVersion(group, version),
			"metadata": map[string]any{"resourceVersion": "1"},
			"items":    items,
		})

	case r.Method == http.MethodGet:
		if _, o := f.getLocked(key, ns, name); o != nil {
			hndWriteJSON(w, 200, o)
			return
		}
		hndStatus(w, 404, "NotFound", resource+" \""+name+"\" not found")

	case r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		var o map[string]any
		if err := json.Unmarshal(raw, &o); err != nil {
			hndStatus(w, 400, "BadRequest", err.Error())
			return
		}
		meta, _ := o["metadata"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
			o["metadata"] = meta
		}
		if res.namespaced && ns != "" {
			meta["namespace"] = ns
		}
		if _, ok := meta["uid"]; !ok {
			meta["uid"] = "uid-created"
		}
		if fakeDryRun(r) {
			// A real API server runs admission and defaulting, then discards.
			hndWriteJSON(w, 201, o)
			return
		}
		res.items = append(res.items, o)
		hndWriteJSON(w, 201, o)

	case r.Method == http.MethodPut:
		raw, _ := io.ReadAll(r.Body)
		var o map[string]any
		if err := json.Unmarshal(raw, &o); err != nil {
			hndStatus(w, 400, "BadRequest", err.Error())
			return
		}
		i, old := f.getLocked(key, ns, name)
		if old == nil {
			hndStatus(w, 404, "NotFound", resource+" \""+name+"\" not found")
			return
		}
		if fakeDryRun(r) {
			hndWriteJSON(w, 200, o)
			return
		}
		res.items[i] = o
		hndWriteJSON(w, 200, o)

	case r.Method == http.MethodPatch:
		_, o := f.getLocked(key, ns, name)
		if o == nil {
			hndStatus(w, 404, "NotFound", resource+" \""+name+"\" not found")
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json-patch+json") {
			if err := hndApplyJSONPatch(o, raw); err != nil {
				hndStatus(w, 400, "BadRequest", err.Error())
				return
			}
		} else {
			var patch map[string]any
			if err := json.Unmarshal(raw, &patch); err != nil {
				hndStatus(w, 400, "BadRequest", err.Error())
				return
			}
			hndMerge(o, patch)
		}
		hndWriteJSON(w, 200, o)

	case r.Method == http.MethodDelete:
		i, o := f.getLocked(key, ns, name)
		if o == nil {
			hndStatus(w, 404, "NotFound", resource+" \""+name+"\" not found")
			return
		}
		res.items = append(res.items[:i], res.items[i+1:]...)
		hndWriteJSON(w, 200, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})

	default:
		hndStatus(w, 405, "MethodNotAllowed", r.Method)
	}
}

// hndMerge is RFC 7386 merge patch, which also covers the strategic patches
// the handlers send (nested maps only, no list merge keys).
func hndMerge(dst, patch map[string]any) {
	for k, v := range patch {
		if v == nil {
			delete(dst, k)
			continue
		}
		if pm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				hndMerge(dm, pm)
				continue
			}
		}
		dst[k] = v
	}
}

// hndApplyJSONPatch supports the add/replace/remove ops the handlers emit.
func hndApplyJSONPatch(obj map[string]any, raw []byte) error {
	var ops []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(raw, &ops); err != nil {
		return err
	}
	for _, op := range ops {
		segs := strings.Split(strings.TrimPrefix(op.Path, "/"), "/")
		cur := obj
		for _, s := range segs[:len(segs)-1] {
			next, ok := cur[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[s] = next
			}
			cur = next
		}
		last := segs[len(segs)-1]
		switch op.Op {
		case "add", "replace":
			cur[last] = op.Value
		case "remove":
			delete(cur, last)
		}
	}
	return nil
}

// hndOpenAPIDoc is a minimal but structurally real OpenAPI v3 slice for
// core/v1, enough for kubectl-explain-style traversal of Pod.
func hndOpenAPIDoc() map[string]any {
	// A real Kubernetes OpenAPI document almost never writes a bare $ref: to
	// hang a description on one it wraps it as
	// {description, default, allOf: [{$ref}]}. The fixture used the bare form,
	// which is why explain's type naming looked correct here while every real
	// cluster reported "Object" for anything with a name.
	ref := func(name string) map[string]any {
		return map[string]any{
			"description": "Reference to " + name,
			"default":     map[string]any{},
			"allOf":       []any{map[string]any{"$ref": "#/components/schemas/" + name}},
		}
	}
	// The bare spelling still occurs, so both have to keep working.
	bareRef := func(name string) map[string]any {
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	return map[string]any{
		"openapi": "3.0.0",
		"components": map[string]any{
			"schemas": map[string]any{
				"io.k8s.api.core.v1.Pod": map[string]any{
					"type":        "object",
					"description": "Pod is a collection of containers.",
					"properties": map[string]any{
						"apiVersion": map[string]any{"type": "string", "description": "API version"},
						"kind":       map[string]any{"type": "string", "description": "Kind"},
						"metadata":   bareRef("io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"),
						"spec":       ref("io.k8s.api.core.v1.PodSpec"),
					},
					"x-kubernetes-group-version-kind": []map[string]any{
						{"group": "", "version": "v1", "kind": "Pod"},
					},
				},
				"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta": map[string]any{
					"type":        "object",
					"description": "Standard object metadata.",
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Name"},
					},
				},
				"io.k8s.api.core.v1.PodSpec": map[string]any{
					"type":        "object",
					"description": "PodSpec is a description of a pod.",
					"required":    []string{"containers"},
					"properties": map[string]any{
						"containers": map[string]any{
							"type": "array", "description": "List of containers.",
							"items": ref("io.k8s.api.core.v1.Container"),
						},
						"nodeName": map[string]any{"type": "string", "description": "Node name"},
					},
				},
				"io.k8s.api.core.v1.Container": map[string]any{
					"type":        "object",
					"description": "A single application container.",
					"required":    []string{"name"},
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "description": "Container name"},
						"image": map[string]any{"type": "string", "description": "Image"},
					},
				},
			},
		},
	}
}

// hndRig is a fully wired API over the fake cluster.
type hndRig struct {
	api    *API
	router http.Handler
	fake   *hndFake
}

// hndNewRig builds the rig the same way server.New does, with OIDC disabled
// (anonymous mode), so no identity provider is needed.
func hndNewRig(t *testing.T) *hndRig {
	t.Helper()
	return hndNewRigWith(t, nil)
}

// hndNewRigWith builds a rig whose configuration a test can adjust before the
// API is constructed — for options that are read at wiring time, like whether
// a route is registered at all.
func hndNewRigWith(t *testing.T, tweak func(*config.Config)) *hndRig {
	t.Helper()

	fake := hndNewFake()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	kc := `apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: ` + srv.URL + `
contexts:
- name: fake
  context:
    cluster: fake
    user: fake
current-context: fake
users:
- name: fake
  user: {}
`
	if err := os.WriteFile(kubeconfig, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}

	webroot := filepath.Join(dir, "web")
	if err := os.MkdirAll(filepath.Join(webroot, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webroot, "index.html"), []byte("<html>orrery-index</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webroot, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Built directly rather than through config.Load so a developer's ORRERY_*
	// environment variables can never change what the test wires up.
	cfg := config.Default()
	cfg.Server.WebRoot = webroot
	cfg.Server.CORSOrigins = []string{"http://cors.example"}
	cfg.Session.EncryptionKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Cache.SyncTimeout = 10 * time.Second
	cfg.Clusters = []config.ClusterConfig{{
		Name:        "fake",
		DisplayName: "Fake cluster",
		Kubeconfig:  kubeconfig,
		AuthMode:    config.AuthModeImpersonation,
		Labels:      map[string]string{"env": "test"},
		QPS:         100,
		Burst:       200,
	}}

	if tweak != nil {
		tweak(cfg)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := cluster.NewRegistry(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)

	sessions, err := auth.NewSessionManager(cfg, auth.NewMemoryStore(cfg.Session.IdleTimeout))
	if err != nil {
		t.Fatal(err)
	}
	anon := &auth.User{
		Username: "orrery:anonymous",
		Name:     "Local user",
		Groups:   []string{"system:authenticated"},
	}
	mw := auth.NewMiddleware(sessions, nil, anon)

	apiSrv := New(cfg, registry, nil, mw, log)
	return &hndRig{api: apiSrv, router: apiSrv.Router(), fake: fake}
}

// do drives one request through the full router.
func (rig *hndRig) do(t *testing.T, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)
	return rec
}

func (rig *hndRig) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return rig.do(t, http.MethodGet, path, "", nil)
}

// hndDecode parses a JSON response body into out, failing loudly on garbage.
func hndDecode(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("bad JSON response (%d): %v\n%s", rec.Code, err, rec.Body.String())
	}
}

// hndWantStatus asserts a status code and prints the body when it differs,
// because the body always names the real failure.
func hndWantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

// fakeDryRun mirrors the API server's dryRun=All handling: do the work, return
// what would have been stored, persist nothing.
func fakeDryRun(r *http.Request) bool {
	for _, v := range r.URL.Query()["dryRun"] {
		if v == "All" {
			return true
		}
	}
	return false
}
