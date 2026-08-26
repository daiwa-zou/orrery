package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// terminalSession is the plumbing under the exec terminal: it is the io.Reader
// the container reads stdin from, the io.Writer its output goes to, and the
// size queue the API server asks for window changes. None of it had a test.
//
// The chunking in Read is the part worth pinning. remotecommand hands it
// whatever buffer it feels like, which is routinely smaller than a frame from
// the browser, so a paste of a few kilobytes arrives as one message and leaves
// across several Reads. Losing the remainder would corrupt input into someone's
// production shell, silently, and only for large pastes.

func newTestSession(t *testing.T) (*terminalSession, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Read, Next and close never touch the socket; the exec handler is what
	// gives them a real one.
	return newTerminalSession(ctx, nil), cancel
}

// readAll drains the session into a string, stopping at EOF.
func readAll(t *testing.T, s *terminalSession, bufSize int) string {
	t.Helper()
	var out strings.Builder
	buf := make([]byte, bufSize)
	for {
		n, err := s.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.String()
		}
		if out.Len() > 1<<20 {
			t.Fatal("read did not terminate")
		}
	}
}

// A frame larger than the caller's buffer must come out whole, in order, once.
func TestTerminalReadSplitsLargeFramesWithoutLoss(t *testing.T) {
	s, cancel := newTestSession(t)

	payload := strings.Repeat("abcdefghij", 500) // 5000 bytes
	s.stdin <- []byte(payload)
	go func() {
		// Give Read time to drain the chunk, then end the stream.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// 7 divides nothing evenly, which is the point: the remainder path runs on
	// almost every iteration.
	if got := readAll(t, s, 7); got != payload {
		t.Errorf("read back %d of %d bytes, and they %s",
			len(got), len(payload),
			map[bool]string{true: "match up to the truncation", false: "do not match"}[strings.HasPrefix(payload, got)])
	}
}

func TestTerminalReadPreservesFrameOrder(t *testing.T) {
	s, cancel := newTestSession(t)

	for _, frame := range []string{"first", "second", "third"} {
		s.stdin <- []byte(frame)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if got := readAll(t, s, 4); got != "firstsecondthird" {
		t.Errorf("got %q", got)
	}
}

// A closed session must end the read rather than park a goroutine on a channel
// nobody will ever send to. close() deliberately cancels instead of closing the
// channel, because readLoop may be mid-send.
func TestTerminalReadEndsAtEOF(t *testing.T) {
	s, _ := newTestSession(t)
	s.close()

	buf := make([]byte, 8)
	n, err := s.Read(buf)
	if n != 0 || err == nil {
		t.Errorf("Read after close = (%d, %v), want (0, EOF)", n, err)
	}
}

// Buffered input is still delivered after close, since it was already accepted
// from the browser; only the wait for *new* input ends.
func TestTerminalReadDrainsPendingAfterClose(t *testing.T) {
	s, _ := newTestSession(t)

	s.stdin <- []byte("hello")
	buf := make([]byte, 2)
	n, err := s.Read(buf)
	if n != 2 || err != nil {
		t.Fatalf("first read = (%d, %v)", n, err)
	}
	s.close()

	// The remainder was already in hand and must not be dropped.
	n, err = s.Read(buf)
	if err != nil || string(buf[:n]) != "ll" {
		t.Errorf("second read = (%q, %v), want the buffered remainder", buf[:n], err)
	}
}

func TestTerminalNextReportsResizes(t *testing.T) {
	s, _ := newTestSession(t)

	s.sizes <- remotecommand.TerminalSize{Width: 120, Height: 40}
	got := s.Next()
	if got == nil || got.Width != 120 || got.Height != 40 {
		t.Fatalf("Next() = %+v, want 120x40", got)
	}
}

// remotecommand blocks on Next for the life of the session, so a closed session
// has to unblock it — a nil return is how it is told to stop.
func TestTerminalNextUnblocksOnClose(t *testing.T) {
	s, _ := newTestSession(t)
	s.close()

	done := make(chan *remotecommand.TerminalSize, 1)
	go func() { done <- s.Next() }()

	select {
	case got := <-done:
		if got != nil {
			t.Errorf("Next() after close = %+v, want nil", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Next() did not return after close; the exec goroutine would leak")
	}
}

// close runs on the read loop's exit, on the handler's defer, and on the
// re-authorization failure path. Cancelling twice is fine; the comment on
// close explains why the channel is never closed instead.
func TestTerminalCloseIsIdempotent(t *testing.T) {
	s, _ := newTestSession(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.close() }()
	}
	wg.Wait()

	if _, err := s.Read(make([]byte, 4)); err == nil {
		t.Error("session still readable after close")
	}
}

// hndTerminalSocket gives a session a real socket, and returns the browser end.
func hndTerminalSocket(t *testing.T) (*terminalSession, *websocket.Conn) {
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
		<-hold // keep the handler alive so the socket stays open
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ws := <-ready
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newTerminalSession(ctx, ws), client
}

// The browser protocol, end to end over a real socket.
func TestTerminalReadLoopDecodesTheClientProtocol(t *testing.T) {
	s, client := hndTerminalSocket(t)
	go s.readLoop()

	send := func(v any) {
		t.Helper()
		if err := client.WriteJSON(v); err != nil {
			t.Fatal(err)
		}
	}

	send(execMessage{Type: "stdin", Data: "ls -l\n"})
	buf := make([]byte, 32)
	n, err := s.Read(buf)
	if err != nil || string(buf[:n]) != "ls -l\n" {
		t.Fatalf("stdin = (%q, %v)", buf[:n], err)
	}

	send(execMessage{Type: "resize", Cols: 100, Rows: 30})
	if got := s.Next(); got == nil || got.Width != 100 || got.Height != 30 {
		t.Fatalf("resize = %+v", got)
	}

	// A degenerate resize is dropped rather than forwarded: a zero-width
	// terminal is not a window size the API server should be told about.
	send(execMessage{Type: "resize", Cols: 0, Rows: 30})
	send(execMessage{Type: "resize", Cols: 80, Rows: 24})
	if got := s.Next(); got == nil || got.Width != 80 {
		t.Fatalf("after a zero resize, Next() = %+v, want the next real one", got)
	}

	// Garbage must not kill the loop; the next good frame still arrives.
	if err := client.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	send(execMessage{Type: "stdin", Data: "still here"})
	n, err = s.Read(buf)
	if err != nil || string(buf[:n]) != "still here" {
		t.Fatalf("after garbage, stdin = (%q, %v)", buf[:n], err)
	}
}

// "close" from the browser ends the loop, which ends the session.
func TestTerminalReadLoopStopsOnClientClose(t *testing.T) {
	s, client := hndTerminalSocket(t)
	done := make(chan struct{})
	go func() { s.readLoop(); close(done) }()

	if err := client.WriteJSON(execMessage{Type: "close"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop ignored the client's close")
	}
	if _, err := s.Read(make([]byte, 4)); err == nil {
		t.Error("the session outlived its read loop")
	}
}

// Container output reaches the browser as a stdout frame.
func TestTerminalWriteSendsStdoutFrames(t *testing.T) {
	s, client := hndTerminalSocket(t)

	n, err := s.Write([]byte("total 0\n"))
	if err != nil || n != len("total 0\n") {
		t.Fatalf("Write = (%d, %v)", n, err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "stdout" || got["data"] != "total 0\n" {
		t.Errorf("frame = %v", got)
	}
}

// newFallbackExecutor exists split out from newExecutor because, in its own
// words, "the transport choice is testable" that way. Nothing was testing it.
func TestNewFallbackExecutorPrefersWebSocketAndStillBuilds(t *testing.T) {
	u, err := url.Parse("https://api.example/api/v1/namespaces/demo/pods/web-1/exec")
	if err != nil {
		t.Fatal(err)
	}
	res := &resolved{clients: &cluster.Clients{Rest: &rest.Config{Host: "https://api.example"}}}

	exec, err := newFallbackExecutor(u, res)
	if err != nil {
		t.Fatalf("building the executor failed: %v", err)
	}
	if exec == nil {
		t.Fatal("no executor returned")
	}
}

// A config the SPDY round-tripper cannot be built from is a failure worth
// surfacing, not a nil executor the handler would dereference.
func TestNewFallbackExecutorReportsATransportItCannotBuild(t *testing.T) {
	u, _ := url.Parse("https://api.example/api/v1/namespaces/demo/pods/web-1/exec")
	res := &resolved{clients: &cluster.Clients{Rest: &rest.Config{
		Host: "https://api.example",
		// Mutually exclusive credentials: client-go refuses to build a
		// transport rather than silently picking one.
		BearerToken:     "token",
		BearerTokenFile: "/nonexistent",
		ExecProvider:    &clientcmdapi.ExecConfig{Command: "true"},
		AuthProvider:    &clientcmdapi.AuthProviderConfig{Name: "oidc"},
	}}}

	exec, err := newFallbackExecutor(u, res)
	if err == nil {
		t.Fatalf("an unbuildable transport returned an executor: %v", exec)
	}
	if exec != nil {
		t.Error("both an executor and an error were returned")
	}
}
