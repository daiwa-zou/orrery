package api

import (
	"reflect"
	"testing"
)

// explainDoc builds an openAPIDoc around the given schemas.
func explainDoc(schemas map[string]*openAPISchema) *openAPIDoc {
	d := &openAPIDoc{}
	d.Components.Schemas = schemas
	return d
}

func TestResolveRef(t *testing.T) {
	target := &openAPISchema{Type: "object", Properties: map[string]*openAPISchema{
		"name": {Type: "string"},
	}}
	d := explainDoc(map[string]*openAPISchema{
		"io.k8s.Target": target,
		"io.k8s.Hop":    {Ref: "#/components/schemas/io.k8s.Target"},
	})

	if got := d.resolveRef(&openAPISchema{Ref: "#/components/schemas/io.k8s.Target"}); got != target {
		t.Error("direct $ref did not resolve")
	}
	// A ref to a ref must resolve transitively.
	if got := d.resolveRef(&openAPISchema{Ref: "#/components/schemas/io.k8s.Hop"}); got != target {
		t.Error("chained $ref did not resolve")
	}

	// allOf with a single member and no own properties is a common way CRDs
	// attach descriptions to a referenced type.
	wrapped := &openAPISchema{AllOf: []*openAPISchema{{Ref: "#/components/schemas/io.k8s.Target"}}}
	if got := d.resolveRef(wrapped); got != target {
		t.Error("single-element allOf did not unwrap")
	}
}

func TestResolveRefDanglingAndCyclic(t *testing.T) {
	d := explainDoc(map[string]*openAPISchema{})

	// A dangling ref returns the schema itself rather than nil, so callers
	// still have something to describe.
	dangling := &openAPISchema{Ref: "#/components/schemas/missing"}
	if got := d.resolveRef(dangling); got != dangling {
		t.Errorf("dangling ref resolved to %+v", got)
	}

	// A self-referential schema must terminate, not loop forever.
	loop := &openAPISchema{}
	loop.Ref = "#/components/schemas/loop"
	d.Components.Schemas["loop"] = loop
	if got := d.resolveRef(loop); got == nil {
		t.Error("cyclic ref resolved to nil")
	}

	if got := d.resolveRef(nil); got != nil {
		t.Errorf("nil schema resolved to %+v", got)
	}
}

func TestSchemaType(t *testing.T) {
	spec := &openAPISchema{Type: "object", Properties: map[string]*openAPISchema{"x": {Type: "string"}}}
	d := explainDoc(map[string]*openAPISchema{
		"io.k8s.api.core.v1.PodSpec": spec,
		// CRDs sometimes publish typeless schemas; the ref name is still the
		// best label available.
		"io.k8s.Untyped": {},
	})

	cases := []struct {
		name string
		in   *openAPISchema
		want string
	}{
		{"scalar", &openAPISchema{Type: "string"}, "string"},
		{"integer", &openAPISchema{Type: "integer"}, "integer"},
		{"array of scalars", &openAPISchema{Type: "array", Items: &openAPISchema{Type: "string"}}, "[]string"},
		{"array without items", &openAPISchema{Type: "array"}, "[]"},
		{
			"map values",
			&openAPISchema{Type: "object", AdditionalProperties: &openAPISchema{Type: "string"}},
			"map[string]string",
		},
		{
			// The ref's last dotted segment names the type, exactly as kubectl
			// explain prints it.
			"ref to named object",
			&openAPISchema{Ref: "#/components/schemas/io.k8s.api.core.v1.PodSpec"},
			"PodSpec",
		},
		{"anonymous object", &openAPISchema{Type: "object"}, "Object"},
		{
			"ref to untyped schema",
			&openAPISchema{Ref: "#/components/schemas/io.k8s.Untyped"},
			"Untyped",
		},
		{"untyped schema", &openAPISchema{}, "Object"},
		{"nil schema", nil, ""},
		{
			"array of refs",
			&openAPISchema{Type: "array", Items: &openAPISchema{Ref: "#/components/schemas/io.k8s.api.core.v1.PodSpec"}},
			"[]PodSpec",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := schemaType(d, tc.in); got != tc.want {
				t.Errorf("schemaType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSortFields(t *testing.T) {
	fields := []explainField{
		{Name: "zeta"},
		{Name: "beta", Required: true},
		{Name: "alpha"},
		{Name: "gamma", Required: true},
	}
	sortFields(fields)

	got := make([]string, len(fields))
	for i, f := range fields {
		got[i] = f.Name
	}
	// Required first, each block alphabetical.
	want := []string{"beta", "gamma", "alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortFields order = %v, want %v", got, want)
	}
}
