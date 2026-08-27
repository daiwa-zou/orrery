package api

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mkObj builds an unstructured object from a metadata fragment plus top-level
// fields, for exercising the per-kind row projectors.
func mkObj(t *testing.T, meta map[string]any, extra map[string]any) *unstructured.Unstructured {
	t.Helper()
	m := map[string]any{"name": "x"}
	for k, v := range meta {
		m[k] = v
	}
	o := map[string]any{"metadata": m}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

// rowFor fetches a registered builtin projector, failing loudly if a kind
// silently drops out of the registry.
func rowFor(t *testing.T, group, kind string) rowFunc {
	t.Helper()
	set, ok := builtinColumns[gk(group, kind)]
	if !ok {
		t.Fatalf("no builtin columns registered for %q", gk(group, kind))
	}
	if len(set.columns) == 0 {
		t.Fatalf("builtin columns for %q are empty", gk(group, kind))
	}
	return set.row
}

func TestIdentityColumns(t *testing.T) {
	nsCols := identityColumns(true)
	if len(nsCols) != 2 || nsCols[0].Key != "name" || nsCols[1].Key != "namespace" {
		t.Errorf("namespaced identity columns = %+v", nsCols)
	}
	// Cluster-scoped tables must not show an always-empty namespace column.
	clusterCols := identityColumns(false)
	if len(clusterCols) != 1 || clusterCols[0].Key != "name" {
		t.Errorf("cluster-scoped identity columns = %+v", clusterCols)
	}
}

func TestBaseRow(t *testing.T) {
	u := mkObj(t, map[string]any{
		"name":              "web",
		"namespace":         "demo",
		"uid":               "abc-123",
		"creationTimestamp": "2024-01-02T03:04:05Z",
	}, nil)

	row := baseRow(u)
	if row["name"] != "web" || row["namespace"] != "demo" || row["uid"] != "abc-123" {
		t.Errorf("identity fields = %v", row)
	}
	if row["age"] != "2024-01-02T03:04:05Z" {
		t.Errorf("age = %v, want the creation timestamp", row["age"])
	}
	if _, ok := row["_terminating"]; ok {
		t.Error("a live object must not be marked terminating")
	}
}

func TestBaseRowClusterScopedAndTerminating(t *testing.T) {
	u := mkObj(t, map[string]any{
		"name":              "node-1",
		"deletionTimestamp": "2024-01-02T03:04:05Z",
	}, nil)

	row := baseRow(u)
	// Cluster-scoped objects omit the key entirely rather than sending "".
	if _, ok := row["namespace"]; ok {
		t.Errorf("cluster-scoped row grew a namespace: %v", row["namespace"])
	}
	if row["_terminating"] != true {
		t.Error("a deleting object should carry the terminating marker")
	}
}

func TestSpecReplicas(t *testing.T) {
	set := mkObj(t, nil, map[string]any{"spec": map[string]any{"replicas": int64(3)}})
	if got := specReplicas(set); got != 3 {
		t.Errorf("specReplicas = %d, want 3", got)
	}
	// An absent field defaults to 1, matching the API server's own default.
	if got := specReplicas(mkObj(t, nil, nil)); got != 1 {
		t.Errorf("specReplicas with no spec = %d, want 1", got)
	}
}

func TestIntOrString(t *testing.T) {
	u := mkObj(t, nil, map[string]any{"spec": map[string]any{
		"str":   "25%",
		"int":   int64(2),
		"float": float64(3),
		"bool":  true,
	}})

	cases := map[string]string{
		"str":     "25%",
		"int":     "2",
		"float":   "3",
		"bool":    "true", // unexpected types still render something
		"missing": "",
	}
	for field, want := range cases {
		if got := intOrString(u, "spec", field); got != want {
			t.Errorf("intOrString(%s) = %q, want %q", field, got, want)
		}
	}
}

func TestFormatSelector(t *testing.T) {
	if got := formatSelector(nil); got != "<all pods>" {
		t.Errorf("empty selector = %q", got)
	}
	// Map iteration order is random; the output must not be.
	got := formatSelector(map[string]string{"b": "2", "a": "1"})
	if got != "a=1, b=2" {
		t.Errorf("selector = %q, want sorted keys", got)
	}
}

func TestJobStatus(t *testing.T) {
	job := func(conds []any, active int64) *unstructured.Unstructured {
		status := map[string]any{}
		if conds != nil {
			status["conditions"] = conds
		}
		if active > 0 {
			status["active"] = active
		}
		return mkObj(t, nil, map[string]any{"status": status})
	}
	cond := func(typ, status string) any {
		return map[string]any{"type": typ, "status": status}
	}

	cases := map[string]struct {
		obj  *unstructured.Unstructured
		want string
	}{
		"complete":  {job([]any{cond("Complete", "True")}, 0), "Complete"},
		"failed":    {job([]any{cond("Failed", "True")}, 0), "Failed"},
		"suspended": {job([]any{cond("Suspended", "True")}, 0), "Suspended"},
		// A condition that is not True must not decide the status.
		"stale false condition": {job([]any{cond("Failed", "False")}, 1), "Running"},
		"active":                {job(nil, 2), "Running"},
		"pending":               {job(nil, 0), "Pending"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := jobStatus(tc.obj); got != tc.want {
				t.Errorf("jobStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJobDuration(t *testing.T) {
	job := func(start, end string) *unstructured.Unstructured {
		status := map[string]any{}
		if start != "" {
			status["startTime"] = start
		}
		if end != "" {
			status["completionTime"] = end
		}
		return mkObj(t, nil, map[string]any{"status": status})
	}

	if got := jobDuration(job("", "")); got != "" {
		t.Errorf("unstarted job duration = %q", got)
	}
	if got := jobDuration(job("2024-01-01T00:00:00Z", "")); got != "running" {
		t.Errorf("running job duration = %q", got)
	}
	if got := jobDuration(job("2024-01-01T00:00:00Z", "2024-01-01T00:05:00Z")); got != "2024-01-01T00:00:00Z|2024-01-01T00:05:00Z" {
		t.Errorf("finished job duration = %q", got)
	}
}

func TestServicePorts(t *testing.T) {
	svc := mkObj(t, nil, map[string]any{"spec": map[string]any{
		"ports": []any{
			map[string]any{"port": int64(80)},
			map[string]any{"port": int64(443), "nodePort": int64(30443)},
			map[string]any{"port": int64(53), "protocol": "UDP"},
			map[string]any{"port": int64(8080), "protocol": "TCP"}, // TCP is the default, not worth printing
		},
	}})
	// kubectl writes the protocol on every port; so does this.
	if got := servicePorts(svc); got != "80/TCP, 443:30443/TCP, 53/UDP, 8080/TCP" {
		t.Errorf("servicePorts = %q", got)
	}
	if got := servicePorts(mkObj(t, nil, nil)); got != "" {
		t.Errorf("portless service = %q", got)
	}
}

func TestPodStatusRemainingBranches(t *testing.T) {
	// An init container that died without a reason still needs a readable
	// status; the exit code is all there is.
	u := mkObj(t, nil, map[string]any{
		"status": map[string]any{
			"phase": "Pending",
			"initContainerStatuses": []any{
				map[string]any{"state": map[string]any{
					"terminated": map[string]any{"exitCode": int64(137)},
				}},
			},
		},
	})
	if got := podStatus(u); got != "Init:ExitCode:137" {
		t.Errorf("reasonless init failure = %q", got)
	}

	// PodInitializing is the normal in-between state, not worth surfacing as a
	// distinct reason.
	u = mkObj(t, nil, map[string]any{
		"status": map[string]any{
			"phase": "Pending",
			"initContainerStatuses": []any{
				map[string]any{"state": map[string]any{
					"waiting": map[string]any{"reason": "PodInitializing"},
				}},
			},
		},
	})
	if got := podStatus(u); got != "Init" {
		t.Errorf("initializing pod = %q", got)
	}

	// A terminated main container's reason (OOMKilled, Completed) explains the
	// pod better than its phase.
	u = mkObj(t, nil, map[string]any{
		"status": map[string]any{
			"phase": "Failed",
			"containerStatuses": []any{
				map[string]any{"state": map[string]any{
					"terminated": map[string]any{"exitCode": int64(137), "reason": "OOMKilled"},
				}},
			},
		},
	})
	if got := podStatus(u); got != "OOMKilled" {
		t.Errorf("terminated container reason = %q", got)
	}
}

func TestNodeStatusWithoutReadyCondition(t *testing.T) {
	// A node that never reported a Ready condition is honestly Unknown.
	u := mkObj(t, nil, map[string]any{
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "MemoryPressure", "status": "False"},
		}},
	})
	if got := nodeStatus(u); got != "Unknown" {
		t.Errorf("conditionless node = %q", got)
	}
}

func TestNodeRolesLegacyLabel(t *testing.T) {
	// Older clusters label roles via kubernetes.io/role=<name> instead of the
	// node-role.kubernetes.io/ prefix.
	u := mkObj(t, map[string]any{
		"labels": map[string]any{"kubernetes.io/role": "master"},
	}, nil)
	if got := nodeRoles(u); len(got) != 1 || got[0] != "master" {
		t.Errorf("legacy role label = %v", got)
	}
}

// ---- per-kind row projectors ----

func TestPodRow(t *testing.T) {
	row := rowFor(t, "", "Pod")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{"nodeName": "node-1"},
		"status": map[string]any{
			"phase": "Running",
			"podIP": "10.0.0.7",
			"containerStatuses": []any{
				map[string]any{"ready": true, "restartCount": int64(3)},
				map[string]any{"ready": false, "restartCount": int64(1)},
			},
		},
	}))
	if row["status"] != "Running" || row["ready"] != "1/2" || row["restarts"] != int64(4) {
		t.Errorf("pod row = %v", row)
	}
	if row["node"] != "node-1" || row["podIP"] != "10.0.0.7" {
		t.Errorf("pod placement = %v", row)
	}
}

func TestDeploymentRow(t *testing.T) {
	row := rowFor(t, "apps", "Deployment")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"image": "nginx:1.27"}},
			}},
		},
		"status": map[string]any{
			"readyReplicas":     int64(2),
			"updatedReplicas":   int64(3),
			"availableReplicas": int64(2),
		},
	}))
	if row["ready"] != "2/3" || row["upToDate"] != int64(3) || row["available"] != int64(2) {
		t.Errorf("deployment row = %v", row)
	}
	if imgs, _ := row["images"].([]string); len(imgs) != 1 || imgs[0] != "nginx:1.27" {
		t.Errorf("deployment images = %v", row["images"])
	}
}

func TestStatefulSetRow(t *testing.T) {
	row := rowFor(t, "apps", "StatefulSet")(mkObj(t, nil, map[string]any{
		"spec":   map[string]any{"replicas": int64(2)},
		"status": map[string]any{"readyReplicas": int64(2)},
	}))
	if row["ready"] != "2/2" {
		t.Errorf("statefulset row = %v", row)
	}
}

func TestDaemonSetRow(t *testing.T) {
	row := rowFor(t, "apps", "DaemonSet")(mkObj(t, nil, map[string]any{
		"status": map[string]any{
			"desiredNumberScheduled": int64(5),
			"currentNumberScheduled": int64(5),
			"numberReady":            int64(4),
			"updatedNumberScheduled": int64(5),
			"numberAvailable":        int64(4),
		},
	}))
	if row["desired"] != int64(5) || row["ready"] != int64(4) || row["available"] != int64(4) {
		t.Errorf("daemonset row = %v", row)
	}
}

func TestReplicaSetAndRCRows(t *testing.T) {
	extra := map[string]any{
		"spec":   map[string]any{"replicas": int64(3)},
		"status": map[string]any{"replicas": int64(3), "readyReplicas": int64(2)},
	}
	for _, k := range []struct{ group, kind string }{
		{"apps", "ReplicaSet"}, {"", "ReplicationController"},
	} {
		row := rowFor(t, k.group, k.kind)(mkObj(t, nil, extra))
		if row["desired"] != int64(3) || row["current"] != int64(3) || row["ready"] != int64(2) {
			t.Errorf("%s row = %v", k.kind, row)
		}
	}
}

func TestJobRow(t *testing.T) {
	row := rowFor(t, "batch", "Job")(mkObj(t, nil, map[string]any{
		"spec":   map[string]any{"completions": int64(5)},
		"status": map[string]any{"succeeded": int64(5), "conditions": []any{map[string]any{"type": "Complete", "status": "True"}}},
	}))
	if row["completions"] != "5/5" || row["status"] != "Complete" {
		t.Errorf("job row = %v", row)
	}

	// spec.completions is optional and defaults to 1, so the denominator must
	// not read 0.
	row = rowFor(t, "batch", "Job")(mkObj(t, nil, map[string]any{
		"status": map[string]any{"succeeded": int64(1)},
	}))
	if row["completions"] != "1/1" {
		t.Errorf("defaulted completions = %v", row["completions"])
	}
}

func TestCronJobRow(t *testing.T) {
	row := rowFor(t, "batch", "CronJob")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{"schedule": "*/5 * * * *", "suspend": true},
		"status": map[string]any{
			"active":           []any{map[string]any{"name": "run-1"}},
			"lastScheduleTime": "2024-06-01T00:00:00Z",
		},
	}))
	if row["schedule"] != "*/5 * * * *" || row["suspend"] != true {
		t.Errorf("cronjob row = %v", row)
	}
	if row["active"] != int64(1) || row["lastSchedule"] != "2024-06-01T00:00:00Z" {
		t.Errorf("cronjob activity = %v", row)
	}
}

func TestServiceRow(t *testing.T) {
	row := rowFor(t, "", "Service")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"type":      "ClusterIP",
			"clusterIP": "10.96.0.10",
			"ports":     []any{map[string]any{"port": int64(80)}},
		},
	}))
	// A port with no protocol in the object still renders as TCP, which is
	// what the API server would have defaulted it to.
	if row["type"] != "ClusterIP" || row["clusterIP"] != "10.96.0.10" || row["ports"] != "80/TCP" {
		t.Errorf("service row = %v", row)
	}
}

func TestEndpointsRow(t *testing.T) {
	row := rowFor(t, "", "Endpoints")(mkObj(t, nil, map[string]any{
		"subsets": []any{map[string]any{
			"addresses": []any{map[string]any{"ip": "10.0.0.1"}, map[string]any{"ip": "10.0.0.2"}},
			"ports":     []any{map[string]any{"port": int64(8080)}},
		}},
	}))
	if row["endpoints"] != "10.0.0.1:8080, 10.0.0.2:8080" {
		t.Errorf("endpoints = %v", row["endpoints"])
	}

	// Addresses without ports still show, as bare IPs.
	row = rowFor(t, "", "Endpoints")(mkObj(t, nil, map[string]any{
		"subsets": []any{map[string]any{
			"addresses": []any{map[string]any{"ip": "10.0.0.9"}},
		}},
	}))
	if row["endpoints"] != "10.0.0.9" {
		t.Errorf("portless endpoints = %v", row["endpoints"])
	}
}

func TestIngressRow(t *testing.T) {
	row := rowFor(t, "networking.k8s.io", "Ingress")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"ingressClassName": "nginx",
			"rules": []any{
				map[string]any{"host": "a.example.com"},
				map[string]any{}, // rule without a host must not add an empty entry
			},
		},
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{
			map[string]any{"ip": "1.2.3.4"},
			map[string]any{"hostname": "lb.example.com"},
		}}},
	}))
	if row["class"] != "nginx" || row["hosts"] != "a.example.com" {
		t.Errorf("ingress row = %v", row)
	}
	if row["address"] != "1.2.3.4, lb.example.com" {
		t.Errorf("ingress address = %v", row["address"])
	}
}

func TestIngressClassRow(t *testing.T) {
	row := rowFor(t, "networking.k8s.io", "IngressClass")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{"controller": "k8s.io/ingress-nginx"},
	}))
	if row["controller"] != "k8s.io/ingress-nginx" {
		t.Errorf("ingressclass row = %v", row)
	}
}

func TestNetworkPolicyRow(t *testing.T) {
	row := rowFor(t, "networking.k8s.io", "NetworkPolicy")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"policyTypes": []any{"Ingress", "Egress"},
		},
	}))
	if row["podSelector"] != "app=web" {
		t.Errorf("networkpolicy selector = %v", row["podSelector"])
	}
	if got, _ := row["types"].([]string); len(got) != 2 {
		t.Errorf("networkpolicy types = %v", row["types"])
	}

	// An empty selector selects everything; the table should say so.
	row = rowFor(t, "networking.k8s.io", "NetworkPolicy")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{"podSelector": map[string]any{}},
	}))
	if row["podSelector"] != "<all pods>" {
		t.Errorf("empty selector = %v", row["podSelector"])
	}
}

func TestConfigMapRow(t *testing.T) {
	row := rowFor(t, "", "ConfigMap")(mkObj(t, nil, map[string]any{
		"data":       map[string]any{"a": "1", "b": "2"},
		"binaryData": map[string]any{"blob": "aGk="},
	}))
	if row["keys"] != int64(3) {
		t.Errorf("configmap keys = %v, want 3 (data + binaryData)", row["keys"])
	}
}

func TestSecretRow(t *testing.T) {
	row := rowFor(t, "", "Secret")(mkObj(t, nil, map[string]any{
		"type": "Opaque",
		"data": map[string]any{"password": "eA=="},
	}))
	if row["type"] != "Opaque" || row["keys"] != int64(1) {
		t.Errorf("secret row = %v", row)
	}

	// Redacted secrets keep their key count via the redaction marker.
	row = rowFor(t, "", "Secret")(mkObj(t, nil, map[string]any{
		"type": "Opaque",
		"orrery.io/redacted": map[string]any{
			"data": map[string]any{"password": "", "token": ""},
		},
	}))
	if row["keys"] != int64(2) {
		t.Errorf("redacted secret keys = %v, want 2", row["keys"])
	}
}

func TestPVCRow(t *testing.T) {
	row := rowFor(t, "", "PersistentVolumeClaim")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"volumeName":       "pv-1",
			"accessModes":      []any{"ReadWriteOnce"},
			"storageClassName": "fast",
		},
		"status": map[string]any{
			"phase":    "Bound",
			"capacity": map[string]any{"storage": "10Gi"},
		},
	}))
	if row["status"] != "Bound" || row["volume"] != "pv-1" || row["capacity"] != "10Gi" {
		t.Errorf("pvc row = %v", row)
	}
	if row["storageClass"] != "fast" {
		t.Errorf("pvc storage class = %v", row["storageClass"])
	}
}

func TestPVRow(t *testing.T) {
	row := rowFor(t, "", "PersistentVolume")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"capacity":                      map[string]any{"storage": "10Gi"},
			"accessModes":                   []any{"ReadWriteOnce"},
			"persistentVolumeReclaimPolicy": "Retain",
			"claimRef":                      map[string]any{"namespace": "demo", "name": "data"},
			"storageClassName":              "fast",
		},
		"status": map[string]any{"phase": "Bound"},
	}))
	if row["claim"] != "demo/data" || row["reclaimPolicy"] != "Retain" {
		t.Errorf("pv row = %v", row)
	}

	// An unbound PV has no claimRef and must not render a bare "/".
	row = rowFor(t, "", "PersistentVolume")(mkObj(t, nil, map[string]any{
		"status": map[string]any{"phase": "Available"},
	}))
	if _, ok := row["claim"]; ok {
		t.Errorf("unbound pv grew a claim: %v", row["claim"])
	}
}

func TestStorageClassRow(t *testing.T) {
	row := rowFor(t, "storage.k8s.io", "StorageClass")(mkObj(t, map[string]any{
		"annotations": map[string]any{"storageclass.kubernetes.io/is-default-class": "true"},
	}, map[string]any{
		"provisioner":       "ebs.csi.aws.com",
		"reclaimPolicy":     "Delete",
		"volumeBindingMode": "WaitForFirstConsumer",
	}))
	if row["provisioner"] != "ebs.csi.aws.com" || row["default"] != true {
		t.Errorf("storageclass row = %v", row)
	}
	if row["bindingMode"] != "WaitForFirstConsumer" {
		t.Errorf("storageclass binding mode = %v", row["bindingMode"])
	}
}

func TestNodeRow(t *testing.T) {
	row := rowFor(t, "", "Node")(mkObj(t, map[string]any{
		"labels": map[string]any{"node-role.kubernetes.io/worker": ""},
	}, map[string]any{
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"nodeInfo":   map[string]any{"kubeletVersion": "v1.30.1", "osImage": "Ubuntu 24.04"},
			"addresses": []any{
				map[string]any{"type": "ExternalIP", "address": "203.0.113.5"},
				map[string]any{"type": "InternalIP", "address": "10.0.0.4"},
			},
		},
	}))
	if row["status"] != "Ready" || row["version"] != "v1.30.1" {
		t.Errorf("node row = %v", row)
	}
	if row["internalIP"] != "10.0.0.4" {
		t.Errorf("node internal IP = %v, want the InternalIP address", row["internalIP"])
	}
}

func TestNamespaceRow(t *testing.T) {
	row := rowFor(t, "", "Namespace")(mkObj(t, nil, map[string]any{
		"status": map[string]any{"phase": "Active"},
	}))
	if row["status"] != "Active" {
		t.Errorf("namespace row = %v", row)
	}
}

func TestEventRow(t *testing.T) {
	row := rowFor(t, "", "Event")(mkObj(t, nil, map[string]any{
		"type":           "Warning",
		"reason":         "BackOff",
		"message":        "Back-off restarting failed container",
		"count":          int64(7),
		"involvedObject": map[string]any{"kind": "Pod", "name": "web-1"},
		"lastTimestamp":  "2024-06-01T00:00:00Z",
	}))
	if row["type"] != "Warning" || row["reason"] != "BackOff" || row["count"] != int64(7) {
		t.Errorf("event row = %v", row)
	}
	if row["object"] != "Pod/web-1" || row["lastSeen"] != "2024-06-01T00:00:00Z" {
		t.Errorf("event identity = %v", row)
	}

	// events.k8s.io-style events only carry eventTime; the column must fall
	// back rather than render empty.
	row = rowFor(t, "", "Event")(mkObj(t, nil, map[string]any{
		"eventTime": "2024-06-02T00:00:00Z",
	}))
	if row["lastSeen"] != "2024-06-02T00:00:00Z" {
		t.Errorf("eventTime fallback = %v", row["lastSeen"])
	}
	row = rowFor(t, "", "Event")(mkObj(t, nil, map[string]any{
		"firstTimestamp": "2024-06-03T00:00:00Z",
	}))
	if row["lastSeen"] != "2024-06-03T00:00:00Z" {
		t.Errorf("firstTimestamp fallback = %v", row["lastSeen"])
	}
}

func TestServiceAccountRow(t *testing.T) {
	row := rowFor(t, "", "ServiceAccount")(mkObj(t, nil, map[string]any{
		"secrets": []any{map[string]any{"name": "token-abc"}},
	}))
	if row["secrets"] != int64(1) {
		t.Errorf("serviceaccount row = %v", row)
	}
}

func TestRBACRows(t *testing.T) {
	roleExtra := map[string]any{"rules": []any{map[string]any{}, map[string]any{}}}
	for _, kind := range []string{"Role", "ClusterRole"} {
		row := rowFor(t, "rbac.authorization.k8s.io", kind)(mkObj(t, nil, roleExtra))
		if row["rules"] != int64(2) {
			t.Errorf("%s rules = %v", kind, row["rules"])
		}
	}

	bindExtra := map[string]any{
		"roleRef": map[string]any{"kind": "ClusterRole", "name": "admin"},
		"subjects": []any{
			map[string]any{"kind": "User", "name": "alice"},
			map[string]any{"kind": "Group", "name": "devs"},
		},
	}
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		row := rowFor(t, "rbac.authorization.k8s.io", kind)(mkObj(t, nil, bindExtra))
		if row["role"] != "ClusterRole/admin" {
			t.Errorf("%s role = %v", kind, row["role"])
		}
		if row["subjects"] != "User/alice, Group/devs" {
			t.Errorf("%s subjects = %v", kind, row["subjects"])
		}
	}
}

func TestHPARow(t *testing.T) {
	row := rowFor(t, "autoscaling", "HorizontalPodAutoscaler")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{"kind": "Deployment", "name": "web"},
			"minReplicas":    int64(2),
			"maxReplicas":    int64(10),
		},
		"status": map[string]any{"currentReplicas": int64(4)},
	}))
	if row["reference"] != "Deployment/web" || row["replicas"] != int64(4) {
		t.Errorf("hpa row = %v", row)
	}
	if row["min"] != int64(2) || row["max"] != int64(10) {
		t.Errorf("hpa bounds = %v", row)
	}
}

func TestPDBRow(t *testing.T) {
	row := rowFor(t, "policy", "PodDisruptionBudget")(mkObj(t, nil, map[string]any{
		"spec":   map[string]any{"minAvailable": int64(1), "maxUnavailable": "25%"},
		"status": map[string]any{"disruptionsAllowed": int64(2)},
	}))
	if row["minAvailable"] != "1" || row["maxUnavailable"] != "25%" || row["allowedDisruptions"] != int64(2) {
		t.Errorf("pdb row = %v", row)
	}
}

func TestPriorityClassRow(t *testing.T) {
	row := rowFor(t, "scheduling.k8s.io", "PriorityClass")(mkObj(t, nil, map[string]any{
		"value":         int64(1000),
		"globalDefault": true,
	}))
	if row["value"] != int64(1000) || row["globalDefault"] != true {
		t.Errorf("priorityclass row = %v", row)
	}
}

func TestCRDRow(t *testing.T) {
	row := rowFor(t, "apiextensions.k8s.io", "CustomResourceDefinition")(mkObj(t, nil, map[string]any{
		"spec": map[string]any{
			"group": "example.com",
			"names": map[string]any{"kind": "Widget"},
			"scope": "Namespaced",
			"versions": []any{
				map[string]any{"name": "v1alpha1"},
				map[string]any{"name": "v1"},
			},
		},
	}))
	if row["group"] != "example.com" || row["kind"] != "Widget" || row["scope"] != "Namespaced" {
		t.Errorf("crd row = %v", row)
	}
	if !reflect.DeepEqual(row["versions"], []string{"v1alpha1", "v1"}) {
		t.Errorf("crd versions = %v", row["versions"])
	}
}

// The case that made the protocol worth showing: a service exposing the same
// port number on two protocols. Without it the column reads "53/UDP, 53",
// which looks like a duplicate rather than the pair it is — and this is not an
// exotic shape, it is what kube-dns looks like on every cluster.
func TestServicePortsDisambiguatesSameNumberOnTwoProtocols(t *testing.T) {
	svc := mkObj(t, nil, map[string]any{
		"spec": map[string]any{"ports": []any{
			map[string]any{"port": int64(53), "protocol": "UDP"},
			map[string]any{"port": int64(53), "protocol": "TCP"},
			map[string]any{"port": int64(9153), "protocol": "TCP"},
		}},
	})
	if got, want := servicePorts(svc), "53/UDP, 53/TCP, 9153/TCP"; got != want {
		t.Errorf("servicePorts = %q, want %q", got, want)
	}
}

// A nodePort keeps the protocol after it, as kubectl writes it.
func TestServicePortsKeepsProtocolAfterNodePort(t *testing.T) {
	svc := mkObj(t, nil, map[string]any{
		"spec": map[string]any{"ports": []any{
			map[string]any{"port": int64(443), "nodePort": int64(30443), "protocol": "TCP"},
			map[string]any{"port": int64(69), "nodePort": int64(30069), "protocol": "UDP"},
		}},
	})
	if got, want := servicePorts(svc), "443:30443/TCP, 69:30069/UDP"; got != want {
		t.Errorf("servicePorts = %q, want %q", got, want)
	}
}
