package api

// The panic recoverer has two jobs, and they pull in opposite directions.
//
// An ordinary panic must not take the process down, so it becomes a logged 500.
// http.ErrAbortHandler must, on the contrary, keep travelling: net/http gives
// that value its meaning in its own deferred recover, where it drops the
// connection without writing a reply. A middleware that catches it first and
// returns normally does not abort anything — the server completes the response
// the handler had abandoned, and the client is handed a truncated body that
// looks whole.

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

func recovererUnderTest() func(http.Handler) http.Handler {
	a := &API{log: slog.New(slog.DiscardHandler), cfg: config.Default()}
	return a.recoverer
}

func TestRecovererTurnsAPanicInto500(t *testing.T) {
	h := recovererUnderTest()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestRecovererLetsErrAbortHandlerThrough(t *testing.T) {
	h := recovererUnderTest()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		switch rec := recover(); rec {
		case nil:
			t.Error("ErrAbortHandler was swallowed; net/http never sees it, " +
				"so the connection is completed rather than dropped")
		case http.ErrAbortHandler:
			// What we want: passed back up untouched.
		default:
			t.Errorf("recovered %v, want it to re-panic with ErrAbortHandler", rec)
		}
	}()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil))
	// Unreachable: ServeHTTP must panic.
	t.Errorf("handler returned normally with status %d", rec.Code)
}
