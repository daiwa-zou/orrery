package api

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ColumnType tells the frontend how to render a cell without the backend
// having to format it, which keeps payloads small and locale handling in one
// place.
type ColumnType string

const (
	ColText   ColumnType = "text"
	ColNumber ColumnType = "number"
	ColAge    ColumnType = "age"    // RFC3339 timestamp; rendered as relative
	ColStatus ColumnType = "status" // rendered as a coloured badge
	ColList   ColumnType = "list"
	ColBool   ColumnType = "bool"
	ColRatio  ColumnType = "ratio" // "2/3"
)

// Column describes one table column.
type Column struct {
	Key   string     `json:"key"`
	Label string     `json:"label"`
	Type  ColumnType `json:"type"`
	// Priority 0 columns always render; higher priorities are dropped first on
	// narrow viewports.
	Priority int    `json:"priority,omitempty"`
	Align    string `json:"align,omitempty"`
}

// rowFunc projects one object into the cells of its row.
type rowFunc func(u *unstructured.Unstructured) map[string]any

// columnSet pairs a table definition with its projector.
type columnSet struct {
	columns []Column
	row     rowFunc
}

// identityColumns are prepended to every table.
func identityColumns(namespaced bool) []Column {
	cols := []Column{{Key: "name", Label: "Name", Type: ColText}}
	if namespaced {
		cols = append(cols, Column{Key: "namespace", Label: "Namespace", Type: ColText})
	}
	return cols
}

var ageColumn = Column{Key: "age", Label: "Age", Type: ColAge, Align: "right"}

// baseRow supplies the fields every row carries, including the identity the
// frontend needs to build links and issue follow-up requests.
func baseRow(u *unstructured.Unstructured) map[string]any {
	row := map[string]any{
		"name": u.GetName(),
		"uid":  string(u.GetUID()),
		"age":  u.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if ns := u.GetNamespace(); ns != "" {
		row["namespace"] = ns
	}
	if ts := u.GetDeletionTimestamp(); ts != nil {
		row["_terminating"] = true
	}
	return row
}

// builtinColumns maps "group/Kind" to a hand-tuned table. Everything not
// listed here still gets a useful table from CRD printer columns or the
// generic fallback.
var builtinColumns = map[string]columnSet{}

func gk(group, kind string) string { return group + "/" + kind }

func init() {
	reg := func(group, kind string, cols []Column, row rowFunc) {
		builtinColumns[gk(group, kind)] = columnSet{columns: cols, row: row}
	}

	// ---- core ----

	reg("", "Pod", []Column{
		{Key: "status", Label: "Status", Type: ColStatus},
		{Key: "ready", Label: "Ready", Type: ColRatio, Align: "right"},
		{Key: "restarts", Label: "Restarts", Type: ColNumber, Align: "right"},
		{Key: "node", Label: "Node", Type: ColText, Priority: 1},
		{Key: "podIP", Label: "IP", Type: ColText, Priority: 2},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		ready, total, restarts := podContainerSummary(u)
		r["status"] = podStatus(u)
		r["ready"] = fmt.Sprintf("%d/%d", ready, total)
		r["restarts"] = restarts
		r["node"] = str(u, "spec", "nodeName")
		r["podIP"] = str(u, "status", "podIP")
		return r
	})

	reg("apps", "Deployment", []Column{
		{Key: "ready", Label: "Ready", Type: ColRatio, Align: "right"},
		{Key: "upToDate", Label: "Up-to-date", Type: ColNumber, Align: "right", Priority: 1},
		{Key: "available", Label: "Available", Type: ColNumber, Align: "right", Priority: 1},
		{Key: "images", Label: "Images", Type: ColList, Priority: 2},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		desired := specReplicas(u)
		r["ready"] = fmt.Sprintf("%d/%d", i64(u, "status", "readyReplicas"), desired)
		r["upToDate"] = i64(u, "status", "updatedReplicas")
		r["available"] = i64(u, "status", "availableReplicas")
		r["images"] = containerImages(u, "spec", "template", "spec")
		return r
	})

	reg("apps", "StatefulSet", []Column{
		{Key: "ready", Label: "Ready", Type: ColRatio, Align: "right"},
		{Key: "images", Label: "Images", Type: ColList, Priority: 2},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["ready"] = fmt.Sprintf("%d/%d", i64(u, "status", "readyReplicas"), specReplicas(u))
		r["images"] = containerImages(u, "spec", "template", "spec")
		return r
	})

	reg("apps", "DaemonSet", []Column{
		{Key: "desired", Label: "Desired", Type: ColNumber, Align: "right"},
		{Key: "current", Label: "Current", Type: ColNumber, Align: "right"},
		{Key: "ready", Label: "Ready", Type: ColNumber, Align: "right"},
		{Key: "upToDate", Label: "Up-to-date", Type: ColNumber, Align: "right", Priority: 1},
		{Key: "available", Label: "Available", Type: ColNumber, Align: "right", Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["desired"] = i64(u, "status", "desiredNumberScheduled")
		r["current"] = i64(u, "status", "currentNumberScheduled")
		r["ready"] = i64(u, "status", "numberReady")
		r["upToDate"] = i64(u, "status", "updatedNumberScheduled")
		r["available"] = i64(u, "status", "numberAvailable")
		return r
	})

	reg("apps", "ReplicaSet", []Column{
		{Key: "desired", Label: "Desired", Type: ColNumber, Align: "right"},
		{Key: "current", Label: "Current", Type: ColNumber, Align: "right"},
		{Key: "ready", Label: "Ready", Type: ColNumber, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["desired"] = specReplicas(u)
		r["current"] = i64(u, "status", "replicas")
		r["ready"] = i64(u, "status", "readyReplicas")
		return r
	})

	reg("", "ReplicationController", []Column{
		{Key: "desired", Label: "Desired", Type: ColNumber, Align: "right"},
		{Key: "current", Label: "Current", Type: ColNumber, Align: "right"},
		{Key: "ready", Label: "Ready", Type: ColNumber, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["desired"] = specReplicas(u)
		r["current"] = i64(u, "status", "replicas")
		r["ready"] = i64(u, "status", "readyReplicas")
		return r
	})

	reg("batch", "Job", []Column{
		{Key: "status", Label: "Status", Type: ColStatus},
		{Key: "completions", Label: "Completions", Type: ColRatio, Align: "right"},
		{Key: "duration", Label: "Duration", Type: ColText, Align: "right", Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		want, ok := i64ok(u, "spec", "completions")
		if !ok {
			want = 1
		}
		r["completions"] = fmt.Sprintf("%d/%d", i64(u, "status", "succeeded"), want)
		r["status"] = jobStatus(u)
		r["duration"] = jobDuration(u)
		return r
	})

	reg("batch", "CronJob", []Column{
		{Key: "schedule", Label: "Schedule", Type: ColText},
		{Key: "suspend", Label: "Suspended", Type: ColBool},
		{Key: "active", Label: "Active", Type: ColNumber, Align: "right"},
		{Key: "lastSchedule", Label: "Last schedule", Type: ColAge, Align: "right", Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["schedule"] = str(u, "spec", "schedule")
		r["suspend"] = boolean(u, "spec", "suspend")
		r["active"] = int64(len(slice(u, "status", "active")))
		r["lastSchedule"] = str(u, "status", "lastScheduleTime")
		return r
	})

	reg("", "Service", []Column{
		{Key: "type", Label: "Type", Type: ColText},
		{Key: "clusterIP", Label: "Cluster IP", Type: ColText},
		{Key: "externalIP", Label: "External IP", Type: ColText, Priority: 1},
		{Key: "ports", Label: "Ports", Type: ColText, Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["type"] = str(u, "spec", "type")
		r["clusterIP"] = str(u, "spec", "clusterIP")
		r["externalIP"] = serviceExternalIP(u)
		r["ports"] = servicePorts(u)
		return r
	})

	reg("", "Endpoints", []Column{
		{Key: "endpoints", Label: "Endpoints", Type: ColText},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		var eps []string
		for _, s := range slice(u, "subsets") {
			sm := mapOf(s)
			addrs, _ := sm["addresses"].([]any)
			ports, _ := sm["ports"].([]any)
			for _, a := range addrs {
				ip := mstr(mapOf(a), "ip")
				if len(ports) == 0 {
					eps = append(eps, ip)
					continue
				}
				for _, p := range ports {
					eps = append(eps, fmt.Sprintf("%s:%d", ip, mint(mapOf(p), "port")))
				}
			}
		}
		r["endpoints"] = joinLimit(eps, 3)
		return r
	})

	reg("networking.k8s.io", "Ingress", []Column{
		{Key: "class", Label: "Class", Type: ColText},
		{Key: "hosts", Label: "Hosts", Type: ColText},
		{Key: "address", Label: "Address", Type: ColText, Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["class"] = str(u, "spec", "ingressClassName")
		var hosts []string
		for _, rule := range slice(u, "spec", "rules") {
			if h := mstr(mapOf(rule), "host"); h != "" {
				hosts = append(hosts, h)
			}
		}
		r["hosts"] = joinLimit(hosts, 3)
		var addrs []string
		for _, ing := range slice(u, "status", "loadBalancer", "ingress") {
			m := mapOf(ing)
			if v := mstr(m, "ip"); v != "" {
				addrs = append(addrs, v)
			} else if v := mstr(m, "hostname"); v != "" {
				addrs = append(addrs, v)
			}
		}
		r["address"] = joinLimit(addrs, 2)
		return r
	})

	reg("networking.k8s.io", "IngressClass", []Column{
		{Key: "controller", Label: "Controller", Type: ColText},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["controller"] = str(u, "spec", "controller")
		return r
	})

	reg("networking.k8s.io", "NetworkPolicy", []Column{
		{Key: "podSelector", Label: "Pod selector", Type: ColText},
		{Key: "types", Label: "Policy types", Type: ColList, Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		sel, _, _ := unstructured.NestedStringMap(u.Object, "spec", "podSelector", "matchLabels")
		r["podSelector"] = formatSelector(sel)
		r["types"] = strSlice(u, "spec", "policyTypes")
		return r
	})

	reg("", "ConfigMap", []Column{
		{Key: "keys", Label: "Keys", Type: ColNumber, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		data, _, _ := unstructured.NestedMap(u.Object, "data")
		bin, _, _ := unstructured.NestedMap(u.Object, "binaryData")
		r["keys"] = int64(len(data) + len(bin))
		return r
	})

	reg("", "Secret", []Column{
		{Key: "type", Label: "Type", Type: ColText},
		{Key: "keys", Label: "Keys", Type: ColNumber, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["type"] = str(u, "type")
		// The cache holds redacted secrets: key names survive, values do not.
		if red, ok, _ := unstructured.NestedMap(u.Object, "orrery.io/redacted", "data"); ok {
			r["keys"] = int64(len(red))
		} else {
			data, _, _ := unstructured.NestedMap(u.Object, "data")
			r["keys"] = int64(len(data))
		}
		return r
	})

	reg("", "PersistentVolumeClaim", []Column{
		{Key: "status", Label: "Status", Type: ColStatus},
		{Key: "volume", Label: "Volume", Type: ColText, Priority: 2},
		{Key: "capacity", Label: "Capacity", Type: ColText, Align: "right"},
		{Key: "accessModes", Label: "Access modes", Type: ColList, Priority: 1},
		{Key: "storageClass", Label: "Storage class", Type: ColText, Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["status"] = str(u, "status", "phase")
		r["volume"] = str(u, "spec", "volumeName")
		r["capacity"] = str(u, "status", "capacity", "storage")
		r["accessModes"] = strSlice(u, "spec", "accessModes")
		r["storageClass"] = str(u, "spec", "storageClassName")
		return r
	})

	reg("", "PersistentVolume", []Column{
		{Key: "status", Label: "Status", Type: ColStatus},
		{Key: "capacity", Label: "Capacity", Type: ColText, Align: "right"},
		{Key: "accessModes", Label: "Access modes", Type: ColList, Priority: 1},
		{Key: "reclaimPolicy", Label: "Reclaim", Type: ColText, Priority: 2},
		{Key: "claim", Label: "Claim", Type: ColText, Priority: 1},
		{Key: "storageClass", Label: "Storage class", Type: ColText, Priority: 2},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["status"] = str(u, "status", "phase")
		r["capacity"] = str(u, "spec", "capacity", "storage")
		r["accessModes"] = strSlice(u, "spec", "accessModes")
		r["reclaimPolicy"] = str(u, "spec", "persistentVolumeReclaimPolicy")
		if ns := str(u, "spec", "claimRef", "namespace"); ns != "" {
			r["claim"] = ns + "/" + str(u, "spec", "claimRef", "name")
		}
		r["storageClass"] = str(u, "spec", "storageClassName")
		return r
	})

	reg("storage.k8s.io", "StorageClass", []Column{
		{Key: "provisioner", Label: "Provisioner", Type: ColText},
		{Key: "default", Label: "Default", Type: ColBool},
		{Key: "reclaimPolicy", Label: "Reclaim", Type: ColText, Priority: 1},
		{Key: "bindingMode", Label: "Binding mode", Type: ColText, Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["provisioner"] = str(u, "provisioner")
		r["reclaimPolicy"] = str(u, "reclaimPolicy")
		r["bindingMode"] = str(u, "volumeBindingMode")
		r["default"] = u.GetAnnotations()["storageclass.kubernetes.io/is-default-class"] == "true"
		return r
	})

	reg("", "Node", []Column{
		{Key: "status", Label: "Status", Type: ColStatus},
		{Key: "roles", Label: "Roles", Type: ColList},
		{Key: "version", Label: "Version", Type: ColText, Priority: 1},
		{Key: "internalIP", Label: "Internal IP", Type: ColText, Priority: 2},
		{Key: "os", Label: "OS image", Type: ColText, Priority: 3},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["status"] = nodeStatus(u)
		r["roles"] = nodeRoles(u)
		r["version"] = str(u, "status", "nodeInfo", "kubeletVersion")
		r["os"] = str(u, "status", "nodeInfo", "osImage")
		for _, a := range slice(u, "status", "addresses") {
			m := mapOf(a)
			if mstr(m, "type") == "InternalIP" {
				r["internalIP"] = mstr(m, "address")
				break
			}
		}
		return r
	})

	reg("", "Namespace", []Column{
		{Key: "status", Label: "Status", Type: ColStatus},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["status"] = str(u, "status", "phase")
		return r
	})

	reg("", "Event", []Column{
		{Key: "type", Label: "Type", Type: ColStatus},
		{Key: "reason", Label: "Reason", Type: ColText},
		{Key: "object", Label: "Object", Type: ColText},
		{Key: "message", Label: "Message", Type: ColText},
		{Key: "count", Label: "Count", Type: ColNumber, Align: "right", Priority: 1},
		{Key: "lastSeen", Label: "Last seen", Type: ColAge, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["type"] = str(u, "type")
		r["reason"] = str(u, "reason")
		r["message"] = str(u, "message")
		r["count"] = i64(u, "count")
		r["object"] = strings.TrimPrefix(
			str(u, "involvedObject", "kind")+"/"+str(u, "involvedObject", "name"), "/")
		last := str(u, "lastTimestamp")
		if last == "" {
			last = str(u, "eventTime")
		}
		if last == "" {
			last = str(u, "firstTimestamp")
		}
		r["lastSeen"] = last
		return r
	})

	reg("", "ServiceAccount", []Column{
		{Key: "secrets", Label: "Secrets", Type: ColNumber, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["secrets"] = int64(len(slice(u, "secrets")))
		return r
	})

	rbacRoleCols := []Column{{Key: "rules", Label: "Rules", Type: ColNumber, Align: "right"}}
	rbacRoleRow := func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["rules"] = int64(len(slice(u, "rules")))
		return r
	}
	reg("rbac.authorization.k8s.io", "Role", rbacRoleCols, rbacRoleRow)
	reg("rbac.authorization.k8s.io", "ClusterRole", rbacRoleCols, rbacRoleRow)

	bindCols := []Column{
		{Key: "role", Label: "Role", Type: ColText},
		{Key: "subjects", Label: "Subjects", Type: ColText},
	}
	bindRow := func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["role"] = str(u, "roleRef", "kind") + "/" + str(u, "roleRef", "name")
		var subs []string
		for _, s := range slice(u, "subjects") {
			m := mapOf(s)
			subs = append(subs, mstr(m, "kind")+"/"+mstr(m, "name"))
		}
		r["subjects"] = joinLimit(subs, 3)
		return r
	}
	reg("rbac.authorization.k8s.io", "RoleBinding", bindCols, bindRow)
	reg("rbac.authorization.k8s.io", "ClusterRoleBinding", bindCols, bindRow)

	reg("autoscaling", "HorizontalPodAutoscaler", []Column{
		{Key: "reference", Label: "Reference", Type: ColText},
		{Key: "replicas", Label: "Replicas", Type: ColNumber, Align: "right"},
		{Key: "min", Label: "Min", Type: ColNumber, Align: "right", Priority: 1},
		{Key: "max", Label: "Max", Type: ColNumber, Align: "right", Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["reference"] = str(u, "spec", "scaleTargetRef", "kind") + "/" + str(u, "spec", "scaleTargetRef", "name")
		r["replicas"] = i64(u, "status", "currentReplicas")
		r["min"] = i64(u, "spec", "minReplicas")
		r["max"] = i64(u, "spec", "maxReplicas")
		return r
	})

	reg("policy", "PodDisruptionBudget", []Column{
		{Key: "minAvailable", Label: "Min available", Type: ColText, Align: "right"},
		{Key: "maxUnavailable", Label: "Max unavailable", Type: ColText, Align: "right"},
		{Key: "allowedDisruptions", Label: "Allowed disruptions", Type: ColNumber, Align: "right"},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["minAvailable"] = intOrString(u, "spec", "minAvailable")
		r["maxUnavailable"] = intOrString(u, "spec", "maxUnavailable")
		r["allowedDisruptions"] = i64(u, "status", "disruptionsAllowed")
		return r
	})

	reg("scheduling.k8s.io", "PriorityClass", []Column{
		{Key: "value", Label: "Value", Type: ColNumber, Align: "right"},
		{Key: "globalDefault", Label: "Global default", Type: ColBool},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["value"] = i64(u, "value")
		r["globalDefault"] = boolean(u, "globalDefault")
		return r
	})

	reg("apiextensions.k8s.io", "CustomResourceDefinition", []Column{
		{Key: "group", Label: "Group", Type: ColText},
		{Key: "kind", Label: "Kind", Type: ColText},
		{Key: "scope", Label: "Scope", Type: ColText},
		{Key: "versions", Label: "Versions", Type: ColList, Priority: 1},
	}, func(u *unstructured.Unstructured) map[string]any {
		r := baseRow(u)
		r["group"] = str(u, "spec", "group")
		r["kind"] = str(u, "spec", "names", "kind")
		r["scope"] = str(u, "spec", "scope")
		var vs []string
		for _, v := range slice(u, "spec", "versions") {
			vs = append(vs, mstr(mapOf(v), "name"))
		}
		r["versions"] = vs
		return r
	})
}

// ---- derived values ----

func specReplicas(u *unstructured.Unstructured) int64 {
	if v, ok := i64ok(u, "spec", "replicas"); ok {
		return v
	}
	return 1
}

func intOrString(u *unstructured.Unstructured, fields ...string) string {
	v, ok, _ := unstructured.NestedFieldNoCopy(u.Object, fields...)
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return itoa(int(t))
	case float64:
		return itoa(int(t))
	}
	return fmt.Sprint(v)
}

func formatSelector(sel map[string]string) string {
	if len(sel) == 0 {
		return "<all pods>"
	}
	keys := make([]string, 0, len(sel))
	for k := range sel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+sel[k])
	}
	return joinLimit(parts, 3)
}

// podContainerSummary counts ready containers and total restarts.
func podContainerSummary(u *unstructured.Unstructured) (ready, total, restarts int64) {
	for _, c := range slice(u, "status", "containerStatuses") {
		m := mapOf(c)
		total++
		if mbool(m, "ready") {
			ready++
		}
		restarts += mint(m, "restartCount")
	}
	if total == 0 {
		total = int64(len(slice(u, "spec", "containers")))
	}
	return ready, total, restarts
}

// podStatus reproduces the phase kubectl shows, which is a good deal more
// informative than status.phase alone: it surfaces the container-level reason
// that actually explains why a pod is not running.
func podStatus(u *unstructured.Unstructured) string {
	if u.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	if reason := str(u, "status", "reason"); reason != "" {
		return reason
	}

	// An init container that is stuck decides the displayed status.
	for _, c := range slice(u, "status", "initContainerStatuses") {
		m := mapOf(c)
		state := mapOf(m["state"])
		if term := mapOf(state["terminated"]); len(term) > 0 {
			if mint(term, "exitCode") == 0 {
				continue
			}
			if r := mstr(term, "reason"); r != "" {
				return "Init:" + r
			}
			return fmt.Sprintf("Init:ExitCode:%d", mint(term, "exitCode"))
		}
		if wait := mapOf(state["waiting"]); len(wait) > 0 {
			if r := mstr(wait, "reason"); r != "" && r != "PodInitializing" {
				return "Init:" + r
			}
			return "Init"
		}
	}

	for _, c := range slice(u, "status", "containerStatuses") {
		m := mapOf(c)
		state := mapOf(m["state"])
		if wait := mapOf(state["waiting"]); len(wait) > 0 {
			if r := mstr(wait, "reason"); r != "" {
				return r
			}
		}
		if term := mapOf(state["terminated"]); len(term) > 0 {
			if r := mstr(term, "reason"); r != "" {
				return r
			}
		}
	}

	phase := str(u, "status", "phase")
	if phase == "" {
		return "Unknown"
	}
	return phase
}

func jobStatus(u *unstructured.Unstructured) string {
	for _, c := range slice(u, "status", "conditions") {
		m := mapOf(c)
		if mstr(m, "status") != "True" {
			continue
		}
		switch mstr(m, "type") {
		case "Complete":
			return "Complete"
		case "Failed":
			return "Failed"
		case "Suspended":
			return "Suspended"
		}
	}
	if i64(u, "status", "active") > 0 {
		return "Running"
	}
	return "Pending"
}

func jobDuration(u *unstructured.Unstructured) string {
	start := str(u, "status", "startTime")
	end := str(u, "status", "completionTime")
	if start == "" {
		return ""
	}
	if end == "" {
		return "running"
	}
	return start + "|" + end // frontend renders the delta
}

func serviceExternalIP(u *unstructured.Unstructured) string {
	var ips []string
	for _, ing := range slice(u, "status", "loadBalancer", "ingress") {
		m := mapOf(ing)
		if v := mstr(m, "ip"); v != "" {
			ips = append(ips, v)
		} else if v := mstr(m, "hostname"); v != "" {
			ips = append(ips, v)
		}
	}
	ips = append(ips, strSlice(u, "spec", "externalIPs")...)
	if len(ips) == 0 {
		if str(u, "spec", "type") == "LoadBalancer" {
			return "<pending>"
		}
		return ""
	}
	return joinLimit(ips, 2)
}

func servicePorts(u *unstructured.Unstructured) string {
	var ports []string
	for _, p := range slice(u, "spec", "ports") {
		m := mapOf(p)
		s := fmt.Sprintf("%d", mint(m, "port"))
		if np := mint(m, "nodePort"); np > 0 {
			s += fmt.Sprintf(":%d", np)
		}
		if proto := mstr(m, "protocol"); proto != "" && proto != "TCP" {
			s += "/" + proto
		}
		ports = append(ports, s)
	}
	return joinLimit(ports, 4)
}

func nodeStatus(u *unstructured.Unstructured) string {
	if boolean(u, "spec", "unschedulable") {
		return "SchedulingDisabled"
	}
	for _, c := range slice(u, "status", "conditions") {
		m := mapOf(c)
		if mstr(m, "type") != "Ready" {
			continue
		}
		switch mstr(m, "status") {
		case "True":
			return "Ready"
		case "False":
			return "NotReady"
		default:
			return "Unknown"
		}
	}
	return "Unknown"
}

func nodeRoles(u *unstructured.Unstructured) []string {
	var roles []string
	for k, v := range u.GetLabels() {
		switch {
		case strings.HasPrefix(k, "node-role.kubernetes.io/"):
			if r := strings.TrimPrefix(k, "node-role.kubernetes.io/"); r != "" {
				roles = append(roles, r)
			}
		case k == "kubernetes.io/role" && v != "":
			roles = append(roles, v)
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		return []string{"<none>"}
	}
	return roles
}
