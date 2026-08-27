package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// explain answers `kubectl explain <resource>[.field...]` from the cluster's
// own OpenAPI v3 document, so the docs always match the server's version and
// cover CRDs that publish a schema.

// explainField is one documented field.
type explainField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// HasChildren tells the UI the field can be drilled into.
	HasChildren bool `json:"hasChildren,omitempty"`
}

type explainResponse struct {
	Kind        string         `json:"kind"`
	FieldPath   string         `json:"fieldPath,omitempty"`
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Fields      []explainField `json:"fields,omitempty"`
}

// openAPIDoc is the slice of an OpenAPI v3 document this feature needs.
type openAPIDoc struct {
	Components struct {
		Schemas map[string]*openAPISchema `json:"schemas"`
	} `json:"components"`
}

type openAPISchema struct {
	Type                 string                    `json:"type"`
	Description          string                    `json:"description"`
	Properties           map[string]*openAPISchema `json:"properties"`
	Items                *openAPISchema            `json:"items"`
	Required             []string                  `json:"required"`
	Ref                  string                    `json:"$ref"`
	AllOf                []*openAPISchema          `json:"allOf"`
	AdditionalProperties *openAPISchema            `json:"additionalProperties"`
	GVKs                 []struct {
		Group   string `json:"group"`
		Version string `json:"version"`
		Kind    string `json:"kind"`
	} `json:"x-kubernetes-group-version-kind"`
}

// resolveRef follows $ref/allOf indirection to the schema that actually
// carries properties.
func (d *openAPIDoc) resolveRef(s *openAPISchema) *openAPISchema {
	for depth := 0; s != nil && depth < 8; depth++ {
		if s.Ref != "" {
			name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
			next, ok := d.Components.Schemas[name]
			if !ok {
				return s
			}
			s = next
			continue
		}
		if len(s.AllOf) == 1 && s.Properties == nil {
			s = s.AllOf[0]
			continue
		}
		return s
	}
	return s
}

// refOf returns the $ref a schema carries.
//
// The Kubernetes document rarely writes a bare reference: to attach a
// description to one it wraps it as {description, default, allOf: [{$ref}]},
// which is the same shape resolveRef already follows. Reading s.Ref alone
// therefore found nothing for almost every field, and the name of the type was
// lost — pod.spec came back as "Object" rather than PodSpec, and
// pod.spec.containers as []Object rather than []Container.
func refOf(s *openAPISchema) string {
	if s == nil {
		return ""
	}
	if s.Ref != "" {
		return s.Ref
	}
	if len(s.AllOf) == 1 && s.Properties == nil {
		return refOf(s.AllOf[0])
	}
	return ""
}

func schemaType(d *openAPIDoc, s *openAPISchema) string {
	r := d.resolveRef(s)
	if r == nil {
		return ""
	}
	switch r.Type {
	case "array":
		if r.Items != nil {
			return "[]" + schemaType(d, r.Items)
		}
		return "[]"
	case "object":
		if r.AdditionalProperties != nil {
			return "map[string]" + schemaType(d, r.AdditionalProperties)
		}
		if ref := refOf(s); ref != "" {
			parts := strings.Split(ref, ".")
			return parts[len(parts)-1]
		}
		return "Object"
	case "":
		if ref := refOf(s); ref != "" {
			parts := strings.Split(ref, ".")
			return parts[len(parts)-1]
		}
		return "Object"
	default:
		return r.Type
	}
}

// explainHandler serves GET /clusters/{cluster}/explain?group&version&kind&field.
func (a *API) explainHandler(w http.ResponseWriter, r *http.Request) {
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	group, version, kind := q.Get("group"), q.Get("version"), q.Get("kind")
	if version == "" || kind == "" {
		a.writeErr(w, r, badRequest("version and kind are required"))
		return
	}
	group = cluster.NormalizeGroup(group)

	// The OpenAPI document is world-readable metadata on every cluster
	// (system:discovery), so no per-user review is needed — there is no
	// object data here, only the API's shape.
	gvPath := "api/" + version
	if group != "" {
		gvPath = "apis/" + group + "/" + version
	}
	paths, err := res.cluster.OpenAPIClient().Paths()
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	gv, ok := paths[gvPath]
	if !ok {
		a.writeErr(w, r, notFound("no OpenAPI document for %s", gvPath))
		return
	}
	raw, err := gv.Schema("application/json")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		a.writeErr(w, r, err)
		return
	}

	// Find the schema whose declared GVK matches.
	var root *openAPISchema
	for _, s := range doc.Components.Schemas {
		for _, gvk := range s.GVKs {
			if gvk.Group == group && gvk.Version == version && strings.EqualFold(gvk.Kind, kind) {
				root = s
				break
			}
		}
		if root != nil {
			break
		}
	}
	if root == nil {
		a.writeErr(w, r, notFound("no schema for %s/%s %s", group, version, kind))
		return
	}

	// Walk the dotted field path, stepping through arrays and maps the same
	// way kubectl explain does.
	//
	// Two schemas are carried, because they answer different questions.
	// `declared` is the field as the document writes it — possibly an array,
	// possibly a $ref — and it is what names the type: kubectl reports
	// pod.spec.containers as []Container, and that is only visible before the
	// array is unwrapped and the reference followed. `cur` is what the field
	// resolves to, and it is what has the properties to list underneath.
	// Reporting the type from `cur` is why every drilled-into field used to
	// come back as the bare word "Object".
	cur := doc.resolveRef(root)
	declared := root
	fieldPath := strings.Trim(q.Get("field"), ".")
	if fieldPath != "" {
		for _, part := range strings.Split(fieldPath, ".") {
			cur = doc.resolveRef(cur)
			for cur != nil && (cur.Type == "array" || cur.AdditionalProperties != nil) {
				if cur.Type == "array" {
					cur = doc.resolveRef(cur.Items)
				} else {
					cur = doc.resolveRef(cur.AdditionalProperties)
				}
			}
			if cur == nil || cur.Properties[part] == nil {
				a.writeErr(w, r, notFound("field %q not found under %s", part, kind))
				return
			}
			declared = cur.Properties[part]
			cur = doc.resolveRef(declared)
		}
	}
	// The type is read before this, from `declared`.
	for cur != nil && cur.Type == "array" {
		cur = doc.resolveRef(cur.Items)
	}
	if cur == nil {
		a.writeErr(w, r, notFound("field path %q not found", fieldPath))
		return
	}

	required := map[string]bool{}
	for _, name := range cur.Required {
		required[name] = true
	}
	out := explainResponse{
		Kind:        kind,
		FieldPath:   fieldPath,
		Type:        schemaType(&doc, declared),
		Description: cur.Description,
	}
	for name, prop := range cur.Properties {
		resolved := doc.resolveRef(prop)
		child := resolved
		for child != nil && (child.Type == "array" || child.AdditionalProperties != nil) {
			if child.Type == "array" {
				child = doc.resolveRef(child.Items)
			} else {
				child = doc.resolveRef(child.AdditionalProperties)
			}
		}
		out.Fields = append(out.Fields, explainField{
			Name:        name,
			Type:        schemaType(&doc, prop),
			Description: resolved.Description,
			Required:    required[name],
			HasChildren: child != nil && len(child.Properties) > 0,
		})
	}
	sortFields(out.Fields)
	writeJSON(w, http.StatusOK, out)
}

func sortFields(fields []explainField) {
	// Required fields first, then alphabetical — the order a reader wants.
	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].Required != fields[j].Required {
			return fields[i].Required
		}
		return fields[i].Name < fields[j].Name
	})
}
