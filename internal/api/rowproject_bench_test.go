package api

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// benchPods builds pods shaped like the ones a projector actually reads: two
// containers apiece, each with the env, mounts and probes that make a real
// container spec large. The size matters — the accessors these projectors use
// used to deep-copy every list they touched, so the cost scaled with what was
// inside the containers rather than with how many there were.
func benchPods(n int) []*unstructured.Unstructured {
	container := func(i int) map[string]any {
		env := make([]any, 0, 12)
		for j := range 12 {
			env = append(env, map[string]any{
				"name":  fmt.Sprintf("SETTING_%d", j),
				"value": fmt.Sprintf("value-%d-%d", i, j),
			})
		}
		mounts := make([]any, 0, 4)
		for j := range 4 {
			mounts = append(mounts, map[string]any{
				"name":      fmt.Sprintf("vol-%d", j),
				"mountPath": fmt.Sprintf("/data/%d", j),
				"readOnly":  j%2 == 0,
			})
		}
		return map[string]any{
			"name":         fmt.Sprintf("app-%d", i),
			"image":        fmt.Sprintf("registry.example/app:%d", i),
			"env":          env,
			"volumeMounts": mounts,
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
				"limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
			},
			"readinessProbe": map[string]any{
				"httpGet": map[string]any{"path": "/healthz", "port": int64(8080)},
			},
		}
	}

	objs := make([]*unstructured.Unstructured, n)
	for i := range objs {
		objs[i] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("web-%d", i),
				"namespace": fmt.Sprintf("ns-%d", i%40),
				"uid":       fmt.Sprintf("uid-%d", i),
			},
			"spec": map[string]any{
				"nodeName":   fmt.Sprintf("node-%d", i%50),
				"containers": []any{container(0), container(1)},
			},
			"status": map[string]any{
				"phase": "Running",
				"podIP": "10.1.2.3",
				"containerStatuses": []any{
					map[string]any{"name": "app-0", "ready": true, "restartCount": int64(i % 5)},
					map[string]any{"name": "app-1", "ready": true, "restartCount": int64(0)},
				},
			},
		}}
	}
	return objs
}

// Projecting a page is what every table request ends in.
//
// The URL carries the parameters a real table page carries, which is not
// decoration: anything read out of the request inside the row loop reparses
// the whole query string, so a bare "/" would measure a version of this
// function that no caller ever reaches.
const benchPageQuery = "/api/v1/clusters/prod/resources/_/v1/pods" +
	"?namespace=payments&namespace=checkout&sort=restarts&order=desc" +
	"&page=1&pageSize=50&labels=true&q=web&view=table"

func BenchmarkProjectPodPage(b *testing.B) {
	objs := benchPods(1_000)
	set := podSet()
	r := httptest.NewRequest("GET", benchPageQuery, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if rows := projectPage(objs, set, 1, 50, r); len(rows) != 50 {
			b.Fatalf("projected %d rows", len(rows))
		}
	}
}

// podRequests runs over every pod in the cluster to build the overview's
// "requested" total, so it reads spec.containers once per pod.
func BenchmarkPodRequests10k(b *testing.B) {
	pods := benchPods(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var cpu, mem int64
		for _, p := range pods {
			c, m := podRequests(p)
			cpu += c
			mem += m
		}
		if cpu == 0 || mem == 0 {
			b.Fatal("the corpus must declare requests")
		}
	}
}
