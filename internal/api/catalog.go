package api

import (
	"net/http"
	"strings"
)

// The capability document describes this server's read-only surface in the
// shape a program needs to call it.
//
// Everything here is also in docs/API.md, which is the right place for a
// person and the wrong place for a client. Something generating tools from
// this API — an MCP server standing in front of it, most obviously — otherwise
// has to hard-code a route table and a parameter list, and then quietly rot
// when a parameter is added. Serving the table means such a client discovers
// the surface at run time and learns what the *server it is actually talking
// to* offers: a build with the proxy disabled does not advertise the proxy,
// and one without OIDC does not advertise a login.
//
// The listing is deliberately not OpenAPI. A schema for these responses would
// be mostly `unstructured`, since the interesting half of every payload is a
// Kubernetes object whose shape comes from the cluster, not from this server —
// and `explain` already serves that half from the cluster's own OpenAPI. What
// a caller cannot discover elsewhere is which routes exist and what narrows
// them, so that is what this describes.
//
// readOnlyEndpoints below is hand-written, which invites drift. It is pinned
// by a test that walks the router and fails when a GET route appears without
// an entry, so the drift is caught at build time rather than by whoever
// trusted the document.

// endpointParam is one knob on an endpoint.
type endpointParam struct {
	Name string `json:"name"`
	// In is "path" or "query".
	In   string `json:"in"`
	Type string `json:"type"`
	// Repeatable marks a query parameter that may be given more than once.
	Repeatable  bool   `json:"repeatable,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

// endpoint describes one route.
type endpoint struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Summary string `json:"summary"`
	// Transport is "websocket" for the streaming routes, empty for plain HTTP.
	Transport string `json:"transport,omitempty"`
	// Produces names the media type when it is not JSON.
	Produces string          `json:"produces,omitempty"`
	Params   []endpointParam `json:"params,omitempty"`
	// requires names the optional feature this route depends on, so a server
	// that does not serve it does not advertise it. Not serialised: an absent
	// endpoint says it better than a flag would.
	requires string
}

type capabilitiesResponse struct {
	BasePath string          `json:"basePath"`
	Features map[string]bool `json:"features"`
	// ReadOnly is every route that only reads. Writes and exec are deliberately
	// absent: this document exists to be handed to a client that must not
	// change anything, and a route it cannot see is a route it cannot call by
	// accident.
	ReadOnly []endpoint `json:"readOnly"`
	// Placeholders explains the two spellings that are not obvious from a path.
	Placeholders map[string]string `json:"placeholders"`
	// Notes are the constraints a caller cannot read off the route table.
	Notes []string `json:"notes"`
}

// pathParamDocs describes the path placeholders once, since the same six
// appear throughout and repeating them per route is how they end up
// disagreeing.
var pathParamDocs = map[string]string{
	"cluster":   "name of a cluster from GET /api/v1/clusters",
	"group":     `API group, or "core" for the legacy group`,
	"version":   "API version, e.g. v1",
	"resource":  "plural resource name, e.g. deployments",
	"namespace": `namespace, or "_" for a cluster-scoped resource`,
	"name":      "object name",
	"ptype":     `"pods" or "services"`,
}

// pathParams derives the path parameters from the route pattern, so a route
// and its documentation cannot disagree about which segments are variable.
func pathParams(path string) []endpointParam {
	var out []endpointParam
	for _, seg := range strings.Split(path, "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		name := strings.Trim(seg, "{}")
		out = append(out, endpointParam{
			Name: name, In: "path", Type: "string", Required: true,
			Description: pathParamDocs[name],
		})
	}
	return out
}

// param builds a query parameter. Named for what it is rather than something
// short: "q" is a local variable in half the handlers in this package, and a
// package-level function by that name is a trap waiting for whoever adds one.
func param(name, typ, def, description string) endpointParam {
	return endpointParam{Name: name, In: "query", Type: typ, Default: def, Description: description}
}

// paramRepeat builds a repeatable query parameter.
func paramRepeat(name, typ, description string) endpointParam {
	return endpointParam{Name: name, In: "query", Type: typ, Repeatable: true, Description: description}
}

// paramRequired builds a required query parameter.
func paramRequired(name, typ, description string) endpointParam {
	return endpointParam{Name: name, In: "query", Type: typ, Required: true, Description: description}
}

// listParams are the filters every table-shaped response accepts.
var listParams = []endpointParam{
	param("namespace", "string", "", "restrict to one namespace; ignored for cluster-scoped resources"),
	param("q", "string", "", "free text over name, namespace and labels (matches key=value)"),
	param("labelSelector", "string", "", "Kubernetes label selector"),
	param("fieldSelector", "string", "", "field selector; unsupported fields are rejected rather than silently matching nothing"),
	param("sort", "string", "name", "column to sort by"),
	param("order", "string", "asc", "asc or desc"),
	param("page", "integer", "1", "1-based page number"),
	param("pageSize", "integer", "50", "rows per page, up to 1000"),
	param("view", "string", "table", "table for projected rows, full for whole objects"),
	param("labels", "boolean", "true", "include each row's labels"),
}

// logParams are shared by the snapshot and single-pod log routes.
var logParams = []endpointParam{
	param("container", "string", "", "container name; defaults to the pod's only or first container"),
	param("tailLines", "integer", "500", "lines from the end, 1 to 100000; there is no unbounded mode"),
	param("sinceSeconds", "integer", "0", "only lines newer than this many seconds"),
	param("previous", "boolean", "false", "read the previous terminated instance"),
	param("timestamps", "boolean", "false", "prefix each line with its timestamp"),
	param("limitBytes", "integer", "0", "stop after this many bytes"),
}

// readOnlyEndpoints is the served read surface. Kept in the order a caller
// meets it: what am I, what is here, what is in it, what is wrong with it.
var readOnlyEndpoints = []endpoint{
	{
		Method: "GET", Path: "/api/v1/healthz",
		Summary: "Liveness. Needs no session.",
	},
	{
		Method: "GET", Path: "/api/v1/auth/config",
		Summary: "What the login page should offer. Needs no session.",
	},
	{
		Method: "GET", Path: "/api/v1/capabilities",
		Summary: "This document.",
	},
	{
		Method: "GET", Path: "/api/v1/me",
		Summary: "The caller as the dashboard resolved them, plus the session's CSRF token.",
	},
	{
		Method: "GET", Path: "/api/v1/clusters",
		Summary: "Every configured cluster and its health, including unreachable ones.",
	},
	{
		Method: "GET", Path: "/api/v1/search",
		Summary: "Find objects by name across clusters, when only the name is known.",
		Params: []endpointParam{
			paramRequired("q", "string", "free text over name, namespace and labels"),
			paramRepeat("cluster", "string", "restrict to these clusters; default is all of them"),
			paramRepeat("resource", "string", `resource to scan, as "resource", "version/resource" or "group/version/resource"`),
			param("namespace", "string", "", "restrict to one namespace"),
			param("limit", "integer", "50", "maximum hits to return, up to 500"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/discovery",
		Summary: "Every resource the cluster serves, grouped, with its verbs and scope.",
		Params: []endpointParam{
			param("allVersions", "boolean", "false", "include non-preferred versions"),
			param("listable", "boolean", "true", "only resources that support list"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/overview",
		Summary: "Cluster landing page: node, namespace, pod and workload counts, capacity against requests, recent warnings.",
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/stats",
		Summary: "What the dashboard is caching for this cluster, limited to resources the caller may list.",
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/events",
		Summary: "Events, newest first, optionally narrowed to one object.",
		Params: []endpointParam{
			param("namespace", "string", "", "restrict to one namespace"),
			param("involvedUID", "string", "", "only events about this object UID"),
			param("involvedName", "string", "", "only events about this object name"),
			param("involvedKind", "string", "", "only events about this kind"),
			param("warningsOnly", "boolean", "false", "drop Normal events"),
			param("q", "string", "", "free text over the projected columns"),
			param("limit", "integer", "200", "maximum events, up to 2000"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/metrics/nodes",
		Summary: "Per-node CPU and memory against capacity. Reports availability rather than failing when metrics-server is absent.",
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/metrics/pods",
		Summary: "Per-pod CPU and memory, with per-container detail and summed limits.",
		Params: []endpointParam{
			param("namespace", "string", "", "restrict to one namespace"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/explain",
		Summary: "Field documentation from the cluster's own OpenAPI, covering CRDs that publish a schema. The kubectl explain equivalent.",
		Params: []endpointParam{
			param("group", "string", "", "API group of the kind"),
			paramRequired("version", "string", "API version of the kind"),
			paramRequired("kind", "string", "kind to explain"),
			param("field", "string", "", "dotted field path to drill into, e.g. spec.containers"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/rollout/history",
		Summary: "A deployment's revisions, newest first, with images and change causes.",
		Params: []endpointParam{
			paramRequired("namespace", "string", "the deployment's namespace"),
			paramRequired("name", "string", "the deployment's name"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/access",
		Summary: "Whether the caller may perform verbs on a resource. Asking changes nothing, so it is a GET and needs no CSRF token.",
		Params: []endpointParam{
			paramRequired("resource", "string", "resource, singular, kind or short name"),
			param("group", "string", "", "API group"),
			param("version", "string", "", "API version; empty means the preferred one"),
			paramRepeat("verb", "string", "verb to test; repeat or comma-separate. Defaults to get, list and watch"),
			param("subresource", "string", "", "subresource, e.g. log or status"),
			param("namespace", "string", "", "namespace to ask about"),
			param("name", "string", "", "object name to ask about"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/access/namespaces",
		Summary: "Which namespaces the caller may read a resource in, so a cluster-wide 403 has a next step.",
		Params: []endpointParam{
			paramRequired("resource", "string", "resource, singular, kind or short name"),
			param("group", "string", "", "API group"),
			param("version", "string", "", "API version"),
			param("verb", "string", "list", "verb to test"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/logs",
		Summary: "Recent logs of up to 20 pods in one JSON reply, without opening a socket.",
		Params: append([]endpointParam{
			paramRequired("namespace", "string", "the pods' namespace"),
			paramRepeat("pod", "string", "pod to read; repeat for a merged answer, up to 20"),
		}, logParams...),
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/pods/{namespace}/{name}/logs",
		Summary:  "One container's logs as plain text.",
		Produces: "text/plain",
		Params: append([]endpointParam{
			param("download", "boolean", "false", "send as an attachment"),
		}, logParams...),
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/pods/{namespace}/{name}/env",
		Summary: "Each container's environment resolved the way the kubelet would, with references the caller may not read reported as errors rather than values.",
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}",
		Summary: "A page of any resource the cluster serves, projected into table columns or returned whole.",
		Params:  listParams,
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}/facets",
		Summary: "The label keys, label values and field-selector values present on the objects the caller may see. A hint for building filters, not an inventory.",
		Params: []endpointParam{
			param("namespace", "string", "", "restrict to one namespace"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}/{namespace}/{name}",
		Summary: "One object, read live rather than from cache.",
		Params: []endpointParam{
			param("format", "string", "json", "yaml to receive the object as YAML"),
			param("managedFields", "boolean", "false", "keep metadata.managedFields"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}/{namespace}/{name}/related",
		Summary: "One object's neighbourhood: owners, children, the node or services it is tied to, the objects it names, and its events.",
		Params: []endpointParam{
			param("depth", "integer", "2", "ownership hops to walk in each direction, 1 to 4"),
			param("events", "boolean", "true", "include the object's events"),
			paramRepeat("childResource", "string", "extra resource to scan for children, for custom controllers"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/proxy/{namespace}/{ptype}/{name}/*",
		Summary:  "Read-only HTTP into a pod or service, the browser's port-forward. GET and HEAD only.",
		Produces: "*/*",
		requires: "proxy",
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/ws/watch/{group}/{version}/{resource}",
		Summary:   "Live updates for a list, filtered by the same parameters the list endpoint takes.",
		Transport: "websocket",
		Params: []endpointParam{
			param("namespace", "string", "", "restrict to one namespace"),
			param("q", "string", "", "free text over name, namespace and labels"),
			param("labelSelector", "string", "", "Kubernetes label selector"),
			param("fieldSelector", "string", "", "field selector"),
		},
	},
	{
		Method: "GET", Path: "/api/v1/clusters/{cluster}/ws/logs",
		Summary:   "Follow one or more pods' logs. For a snapshot, prefer the plain GET at /logs.",
		Transport: "websocket",
		Params: append([]endpointParam{
			paramRequired("namespace", "string", "the pods' namespace"),
			paramRepeat("pod", "string", "pod to follow, up to 20"),
		}, logParams...),
	},
}

// capabilities serves the read-only surface this build actually offers.
func (a *API) capabilities(w http.ResponseWriter, r *http.Request) {
	features := map[string]bool{
		"proxy":     a.cfg.Proxy.ProxyEnabled(),
		"oidc":      a.cfg.OIDC.Enabled,
		"anonymous": a.mw.Anonymous(),
	}

	out := capabilitiesResponse{
		BasePath: "/api/v1",
		Features: features,
		ReadOnly: make([]endpoint, 0, len(readOnlyEndpoints)),
		Placeholders: map[string]string{
			"group":     `"core" stands for the legacy API group`,
			"namespace": `"_" stands for cluster scope`,
		},
		Notes: []string{
			"Every read is preceded by a SubjectAccessReview for the calling user; a 403 here means the cluster's RBAC said no, not that the object is missing.",
			"Everything below /me requires a session cookie. healthz and auth/config do not.",
			"Write and exec routes exist but are not listed here; they need the CSRF token from /me.",
			"A 499 means the client hung up, and is not a server failure.",
		},
	}
	for _, e := range readOnlyEndpoints {
		if e.requires != "" && !features[e.requires] {
			continue
		}
		e.Params = append(pathParams(e.Path), e.Params...)
		out.ReadOnly = append(out.ReadOnly, e)
	}
	writeJSON(w, http.StatusOK, out)
}
