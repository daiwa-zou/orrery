package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"

	"github.com/gorilla/websocket"
)

// wsPair puts a real socket between a wsConn and a client, because what these
// helpers do is write frames, and a test that did not read them back would be
// asserting that the calls returned rather than that anything arrived.
func wsPair(t *testing.T) (*wsConn, *websocket.Conn) {
	t.Helper()
	var (
		up      = websocket.Upgrader{}
		ready   = make(chan *wsConn, 1)
		hold    = make(chan struct{})
		closeIt sync.Once
	)
	t.Cleanup(func() { closeIt.Do(func() { close(hold) }) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ready <- newWSConn(c)
		<-hold
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return <-ready, client
}

// readClose drains frames until the close arrives, returning its code and
// reason. Anything read on the way is handed back so a caller can check what
// preceded the close.
func readClose(t *testing.T, client *websocket.Conn) (code int, reason string, before []string) {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := client.ReadMessage()
		if err == nil {
			before = append(before, string(data))
			continue
		}
		ce := &websocket.CloseError{}
		if !asCloseError(err, ce) {
			t.Fatalf("read ended with %v, want a close frame", err)
		}
		return ce.Code, ce.Text, before
	}
}

func asCloseError(err error, out *websocket.CloseError) bool {
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		return false
	}
	*out = *ce
	return true
}

// A stream that ends has a reason, and the whole point of these two helpers is
// that the reason reaches the browser. A socket that just dropped would leave
// the UI to invent one.
func TestCloseWithSendsTheReasonToTheClient(t *testing.T) {
	ws, client := wsPair(t)

	ws.closeWith(websocket.CloseNormalClosure, "log stream ended")

	code, reason, _ := readClose(t, client)
	if code != websocket.CloseNormalClosure {
		t.Errorf("close code = %d, want %d", code, websocket.CloseNormalClosure)
	}
	if reason != "log stream ended" {
		t.Errorf("close reason = %q, want %q", reason, "log stream ended")
	}
}

// wsError writes the stream's own JSON envelope before closing, because that
// is the message the UI renders; the close frame is the backstop for a client
// that never read it.
func TestWSErrorSendsTheEnvelopeBeforeTheClose(t *testing.T) {
	ws, client := wsPair(t)

	ws.wsError("access to this pod's logs was revoked")

	code, reason, before := readClose(t, client)

	if len(before) != 1 {
		t.Fatalf("got %d frames before the close, want 1: %q", len(before), before)
	}
	var msg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(before[0]), &msg); err != nil {
		t.Fatalf("the frame before the close was not JSON: %v (%q)", err, before[0])
	}
	if msg.Type != "ERROR" {
		t.Errorf("frame type = %q, want ERROR", msg.Type)
	}
	if msg.Message != "access to this pod's logs was revoked" {
		t.Errorf("frame message = %q", msg.Message)
	}
	if code != websocket.CloseInternalServerErr {
		t.Errorf("close code = %d, want %d", code, websocket.CloseInternalServerErr)
	}
	if reason != msg.Message {
		t.Errorf("close reason %q does not match the envelope %q", reason, msg.Message)
	}
}

// A close payload is 125 bytes including the two-byte code. A reason longer
// than that has to be cut, and cut somewhere legal: a frame the peer rejects
// as a protocol error loses the very sentence it was carrying.
func TestCloseWithTruncatesAnOversizedReason(t *testing.T) {
	ws, client := wsPair(t)

	// Multi-byte runes so the cut has a partial one to deal with.
	long := strings.Repeat("é", 200)
	ws.closeWith(websocket.CloseInternalServerErr, long)

	code, reason, _ := readClose(t, client)
	if code != websocket.CloseInternalServerErr {
		t.Errorf("close code = %d, want %d", code, websocket.CloseInternalServerErr)
	}
	if len(reason) > 123 {
		t.Errorf("close reason is %d bytes, which will not fit the frame", len(reason))
	}
	if !strings.HasPrefix(long, reason) {
		t.Errorf("the reason was rewritten rather than cut: %q", reason)
	}
	// strings.ToValidUTF8 drops the partial rune rather than sending a
	// mangled one.
	if strings.ContainsRune(reason, '�') {
		t.Errorf("a partial rune survived into %q", reason)
	}
}

func TestWSConnDoneAndCloseAreIdempotent(t *testing.T) {
	ws, _ := wsPair(t)

	select {
	case <-ws.Done():
		t.Fatal("Done was closed before anything closed the socket")
	default:
	}

	ws.close()
	select {
	case <-ws.Done():
	case <-time.After(time.Second):
		t.Fatal("Done was not closed by close()")
	}

	// A stream can be torn down from more than one goroutine — the reauth
	// ticker and the reader both call it — so a second close must not panic
	// on an already-closed channel.
	ws.close()
	ws.closeWith(websocket.CloseNormalClosure, "again")
	ws.wsError("and again")
}

// Writing after the peer has gone reports the failure rather than blocking or
// panicking; the log stream checks that return to decide whether to keep
// reading from the cluster.
func TestWriteJSONAfterCloseReportsTheError(t *testing.T) {
	ws, _ := wsPair(t)
	ws.close()

	if err := ws.WriteJSON(map[string]any{"type": "LOG"}); err == nil {
		t.Error("WriteJSON on a closed socket returned no error")
	}
}

// A value that cannot be marshalled must not reach the socket: half a frame is
// worse than none, and the caller needs to know its message was not sent.
func TestWriteJSONRejectsAnUnmarshalableValue(t *testing.T) {
	ws, client := wsPair(t)

	if err := ws.WriteJSON(map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("WriteJSON accepted a value it cannot encode")
	}

	// Nothing was written, so an ordinary frame after it is the first one the
	// client sees.
	if err := ws.WriteJSON(map[string]any{"type": "LOG"}); err != nil {
		t.Fatalf("the socket was left unusable: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"LOG"`) {
		t.Errorf("first frame was %q, want the LOG frame", data)
	}
}

// ping outlives nothing: it stops when the request context is cancelled and
// when the socket closes, and a goroutine that kept ticking on either would
// hold the connection open past the handler that owns it.
func TestPingStopsWithTheContext(t *testing.T) {
	ws, _ := wsPair(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { ws.ping(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ping did not return when its context was cancelled")
	}
}

func TestPingStopsWhenTheSocketCloses(t *testing.T) {
	ws, _ := wsPair(t)

	done := make(chan struct{})
	go func() { ws.ping(context.Background()); close(done) }()

	ws.close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ping did not return when the socket closed")
	}
}

// drain exists so control frames are processed on streams that expect no
// client input, and it closes the connection when the peer goes away — which
// is what makes Done fire for a browser that navigated off.
func TestDrainClosesWhenThePeerGoesAway(t *testing.T) {
	ws, client := wsPair(t)

	go ws.drain()
	_ = client.Close()

	select {
	case <-ws.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not close the connection after the peer went away")
	}
}
