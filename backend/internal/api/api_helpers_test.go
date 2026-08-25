package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/daiwa-zou/orrery/backend/internal/cluster"
)

// quietAPI is the smallest API that can serve writeErr: it only touches a.log.
func quietAPI() *API {
	return &API{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func decodeErrBody(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the error shape: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]string{"ok": "yes"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
	// Responses can carry live secrets; caching them anywhere is a leak.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache control = %q", cc)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["ok"] != "yes" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestWriteJSONSurvivesUnencodableBody(t *testing.T) {
	rec := httptest.NewRecorder()
	// A channel cannot be marshalled; the status line is already out, so the
	// only correct behaviour is not to panic.
	writeJSON(rec, http.StatusOK, map[string]any{"bad": make(chan int)})
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestWriteErrZeroCodeStatusError(t *testing.T) {
	// A Kubernetes status error with no code must not become an HTTP 0.
	rec := httptest.NewRecorder()
	quietAPI().writeErr(rec, req(""), &apierrors.StatusError{
		ErrStatus: metav1.Status{Message: "odd upstream failure"},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := decodeErrBody(t, rec); body.Error != "kubernetes" {
		t.Errorf("error kind = %q, want kubernetes", body.Error)
	}
}

func TestWriteErrNilWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	quietAPI().writeErr(rec, req(""), nil)
	if rec.Body.Len() != 0 {
		t.Errorf("nil error produced a body: %q", rec.Body.String())
	}
}

func TestWriteErrMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantKind   string
	}{
		// A cancelled request is the browser's doing, not a server failure.
		{"client cancel", fmt.Errorf("list: %w", context.Canceled), 499, "client_closed_request"},
		{"forbidden", &forbiddenError{verb: "get", resource: "secrets"}, http.StatusForbidden, "forbidden"},
		// Kubernetes status errors pass their own code and reason through.
		{
			"kubernetes 404",
			apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "web"),
			http.StatusNotFound, "notfound",
		},
		{
			"kubernetes 409",
			apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "web", errors.New("stale")),
			http.StatusConflict, "conflict",
		},
		{"unknown cluster", &cluster.UnknownClusterError{Name: "prod"}, http.StatusNotFound, "unknown_cluster"},
		{"unknown resource", &cluster.UnknownResourceError{}, http.StatusNotFound, "unknown_resource"},
		{"bad request", badRequest("no such thing"), http.StatusBadRequest, "bad_request"},
		{"not found", notFound("no %s", "pod"), http.StatusNotFound, "not_found"},
		{"anything else", errors.New("boom"), http.StatusInternalServerError, "internal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			quietAPI().writeErr(rec, req(""), tc.err)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			body := decodeErrBody(t, rec)
			if body.Error != tc.wantKind {
				t.Errorf("error kind = %q, want %q", body.Error, tc.wantKind)
			}
			if body.Code != tc.wantStatus {
				t.Errorf("body code = %d, want %d", body.Code, tc.wantStatus)
			}
		})
	}
}

func TestBadRequestAndNotFoundFormat(t *testing.T) {
	err := badRequest("field %q is bad", "x")
	if !errors.Is(err, errBadRequest) {
		t.Error("badRequest lost its sentinel")
	}
	if got := err.Error(); got != `bad request: field "x" is bad` {
		t.Errorf("message = %q", got)
	}

	err = notFound("pod %s", "web")
	if !errors.Is(err, errNotFound) {
		t.Error("notFound lost its sentinel")
	}
	if got := err.Error(); got != "not found: pod web" {
		t.Errorf("message = %q", got)
	}
}

func TestForbiddenErrorIncludesReason(t *testing.T) {
	e := &forbiddenError{verb: "get", resource: "secrets", namespace: "demo", reason: "RBAC denied"}
	want := "you are not allowed to get secrets in namespace demo (RBAC denied)"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
}

func TestQueryBool(t *testing.T) {
	r := req("t1=true&t2=1&t3=yes&f1=false&f2=0&f3=whatever")
	for _, key := range []string{"t1", "t2", "t3"} {
		if !queryBool(r, key, false) {
			t.Errorf("%s should read as true", key)
		}
	}
	for _, key := range []string{"f1", "f2", "f3"} {
		if queryBool(r, key, true) {
			t.Errorf("%s should read as false", key)
		}
	}
	// Only an absent parameter uses the default.
	if !queryBool(r, "missing", true) || queryBool(r, "missing", false) {
		t.Error("missing param did not fall back to the default")
	}
}
