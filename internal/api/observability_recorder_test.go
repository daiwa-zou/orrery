package api

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// statusRecorder wraps every response so the metrics middleware can label it
// by status. Wrapping a ResponseWriter is the classic way to break streaming
// and WebSockets, because the handler downstream reaches for Flusher and
// Hijacker on the wrapper and does not find them — so what is tested here is
// mostly that nothing was lost on the way through.

func TestStatusRecorderRemembersTheFirstStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec}

	s.WriteHeader(http.StatusTeapot)
	// A handler that writes a header twice gets one warning from net/http and
	// one status on the wire; the label has to be the one that was sent.
	s.WriteHeader(http.StatusInternalServerError)

	if s.status != http.StatusTeapot {
		t.Errorf("status = %d, want %d", s.status, http.StatusTeapot)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("the response carried %d, want %d", rec.Code, http.StatusTeapot)
	}
}

// A handler that writes a body without a status has sent a 200, and the label
// has to say so rather than staying at the zero value.
func TestStatusRecorderInfersTwoHundredFromAWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec}

	n, err := s.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if s.status != http.StatusOK {
		t.Errorf("status = %d, want 200", s.status)
	}

	// A second write must not move it.
	if _, err := s.Write([]byte(" again")); err != nil {
		t.Fatal(err)
	}
	if s.status != http.StatusOK {
		t.Errorf("status = %d after a second write, want 200", s.status)
	}
	if got := rec.Body.String(); got != "hello again" {
		t.Errorf("body = %q, want %q", got, "hello again")
	}
}

func TestStatusRecorderKeepsAnExplicitStatusThroughAWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec}

	s.WriteHeader(http.StatusNotFound)
	if _, err := s.Write([]byte(`{"error":"not_found"}`)); err != nil {
		t.Fatal(err)
	}

	if s.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404: a write must not overwrite the status", s.status)
	}
}

// flushRecorder counts flushes, which httptest.ResponseRecorder does not
// report on its own.
type flushRecorder struct {
	http.ResponseWriter
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++ }

// Every stream in this server flushes: log follows, watch snapshots, the
// event feed. A wrapper that swallowed Flush would buffer them all until the
// response ended, which for a follow is never.
func TestStatusRecorderPassesFlushThrough(t *testing.T) {
	inner := &flushRecorder{ResponseWriter: httptest.NewRecorder()}
	s := &statusRecorder{ResponseWriter: inner}

	s.Flush()
	s.Flush()

	if inner.flushes != 2 {
		t.Errorf("the wrapped writer saw %d flushes, want 2", inner.flushes)
	}
}

// A writer that cannot flush is not an error — the wrapper simply has nothing
// to pass the call to, and must not panic asserting otherwise.
func TestStatusRecorderFlushWithoutAFlusher(t *testing.T) {
	s := &statusRecorder{ResponseWriter: nonFlusher{}}
	s.Flush()
}

type nonFlusher struct{}

func (nonFlusher) Header() http.Header         { return http.Header{} }
func (nonFlusher) Write(b []byte) (int, error) { return len(b), nil }
func (nonFlusher) WriteHeader(int)             {}

// http.ResponseController finds the real writer by unwrapping, which is how a
// handler sets a read or write deadline through the middleware stack.
func TestStatusRecorderUnwrapsToTheRealWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec}

	if got := s.Unwrap(); got != http.ResponseWriter(rec) {
		t.Errorf("Unwrap returned %#v, want the wrapped writer", got)
	}
}

// The WebSocket upgrader takes the connection over by asserting for
// http.Hijacker. Without this passing through, every stream fails its
// handshake — and the upgrader answers the client directly, so the server logs
// nothing that would explain it.
func TestStatusRecorderHijacksThroughToTheConnection(t *testing.T) {
	hj := &hijackRecorder{ResponseWriter: httptest.NewRecorder()}
	s := &statusRecorder{ResponseWriter: hj}

	conn, rw, err := s.Hijack()
	if err != nil {
		t.Fatalf("Hijack errored: %v", err)
	}
	if !hj.hijacked {
		t.Error("the wrapped writer was never asked to hijack")
	}
	if conn != hj.conn || rw != hj.rw {
		t.Error("Hijack did not return what the wrapped writer gave it")
	}
}

// A writer that cannot be hijacked says so in a sentence naming the type,
// because the alternative — a nil connection and a nil error — crashes the
// upgrader somewhere else entirely.
func TestStatusRecorderHijackWithoutAHijacker(t *testing.T) {
	s := &statusRecorder{ResponseWriter: httptest.NewRecorder()}

	conn, rw, err := s.Hijack()
	if err == nil {
		t.Fatal("Hijack on a non-hijacker returned no error")
	}
	if conn != nil || rw != nil {
		t.Error("Hijack returned a connection alongside its error")
	}
	// Naming the type is the whole value of the message: the reader is looking
	// at a failed handshake and needs to know which wrapper in the stack ate
	// the Hijacker.
	if !strings.Contains(err.Error(), "ResponseRecorder") {
		t.Errorf("the error %q does not name the writer that cannot hijack", err)
	}
}

type hijackRecorder struct {
	http.ResponseWriter
	hijacked bool
	conn     net.Conn
	rw       *bufio.ReadWriter
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	client, server := net.Pipe()
	_ = client.Close()
	h.conn = server
	h.rw = bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server))
	return h.conn, h.rw, nil
}
