package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
)

// execMessage is the browser-facing terminal protocol. It is deliberately
// small: stdin, resize, and a close.
type execMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// terminalSession bridges a WebSocket to a container's stdio. It implements
// io.Reader (stdin), io.Writer (stdout/stderr) and remotecommand's
// TerminalSizeQueue.
type terminalSession struct {
	ws     *wsConn
	ctx    context.Context
	cancel context.CancelFunc

	// stdin carries decoded input from the browser to the container.
	stdin chan []byte
	// pending holds the remainder of a chunk that did not fit the caller's
	// buffer, since Read may be given a smaller slice than the frame.
	pending []byte

	sizes chan remotecommand.TerminalSize

	closeOnce sync.Once
}

func newTerminalSession(ctx context.Context, ws *wsConn) *terminalSession {
	c, cancel := context.WithCancel(ctx)
	return &terminalSession{
		ws:     ws,
		ctx:    c,
		cancel: cancel,
		stdin:  make(chan []byte, 64),
		sizes:  make(chan remotecommand.TerminalSize, 4),
	}
}

// readLoop decodes client frames until the socket closes.
func (t *terminalSession) readLoop() {
	defer t.close()
	for {
		_, raw, err := t.ws.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg execMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "stdin":
			select {
			case t.stdin <- []byte(msg.Data):
			case <-t.ctx.Done():
				return
			}
		case "resize":
			if msg.Cols == 0 || msg.Rows == 0 {
				continue
			}
			select {
			case t.sizes <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}:
			default:
				// A resize storm is not worth blocking on; the next one wins.
			}
		case "close":
			return
		}
	}
}

// Read supplies the container's stdin.
func (t *terminalSession) Read(p []byte) (int, error) {
	if len(t.pending) > 0 {
		n := copy(p, t.pending)
		t.pending = t.pending[n:]
		return n, nil
	}
	select {
	case <-t.ctx.Done():
		return 0, io.EOF
	case chunk := <-t.stdin:
		n := copy(p, chunk)
		if n < len(chunk) {
			t.pending = chunk[n:]
		}
		return n, nil
	}
}

// Write forwards the container's output to the browser.
func (t *terminalSession) Write(p []byte) (int, error) {
	if err := t.ws.WriteJSON(map[string]any{"type": "stdout", "data": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Next reports terminal resizes to the API server.
func (t *terminalSession) Next() *remotecommand.TerminalSize {
	select {
	case size := <-t.sizes:
		return &size
	case <-t.ctx.Done():
		return nil
	}
}

func (t *terminalSession) close() {
	// The channel is deliberately never closed: readLoop may be blocked on a
	// send at this exact moment, and a send racing a close panics the whole
	// process. Cancelling the context is enough — Read returns io.EOF and the
	// parked sender's ctx.Done() case fires.
	t.closeOnce.Do(t.cancel)
}

// execIntoPod opens an interactive shell in a container.
//
// The exec runs with the caller's own identity: in impersonation mode the API
// server's audit log records the real user, not the dashboard's service
// account. That property is the reason impersonation is the default.
func (a *API) execIntoPod(w http.ResponseWriter, r *http.Request) {
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	q := r.URL.Query()
	namespace, pod, container := q.Get("namespace"), q.Get("pod"), q.Get("container")
	if namespace == "" || pod == "" {
		a.writeErr(w, r, badRequest("namespace and pod are required"))
		return
	}
	command := q["command"]
	if len(command) == 0 {
		// Try a real shell first and fall back to sh, which is what every
		// terminal UI ends up doing anyway.
		command = []string{"/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}
	}

	podRes, err := res.cluster.Discovery.Resolve(r.Context(), "", "v1", "pods")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res.resource = podRes
	if err := a.authorize(r.Context(), res, "create", namespace, pod, "exec"); err != nil {
		a.writeErr(w, r, err)
		return
	}

	conn, err := a.upgrader().Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws := newWSConn(conn)
	defer ws.close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go ws.ping(ctx)

	session := newTerminalSession(ctx, ws)
	go session.readLoop()

	// An interactive shell is the most privileged stream the dashboard offers;
	// like watches, it must not outlive the permission — or the session — that
	// opened it. The goroutine works on its own copy of res because the main
	// goroutine still reads res for the executor.
	reauthRes := *res
	go func() {
		t := time.NewTicker(reauthorizeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := a.refreshStreamIdentity(ctx, r, &reauthRes); err != nil {
					ws.wsError("session expired; sign in again")
					cancel()
					return
				}
				if err := a.authorize(ctx, &reauthRes, "create", namespace, pod, "exec"); err != nil {
					ws.wsError(streamClosedBecause(err, "access to this pod was revoked"))
					cancel()
					return
				}
			}
		}
	}()

	executor, err := a.newExecutor(res, namespace, pod, container, command)
	if err != nil {
		ws.wsError(err.Error())
		return
	}

	_ = ws.WriteJSON(map[string]any{"type": "open", "container": container, "command": command})

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             session,
		Stdout:            session,
		Stderr:            session,
		Tty:               true,
		TerminalSizeQueue: session,
	})
	session.close()

	if err != nil && !isClientGone(err) {
		_ = ws.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
		ws.closeWith(websocket.CloseInternalServerErr, err.Error())
		return
	}
	_ = ws.WriteJSON(map[string]any{"type": "exit"})
	ws.closeWith(websocket.CloseNormalClosure, "session ended")
}

// newExecutor prefers the WebSocket transport the API server has offered since
// 1.29 and falls back to SPDY for older clusters.
func (a *API) newExecutor(res *resolved, namespace, pod, container string, command []string) (remotecommand.Executor, error) {
	req := res.clients.Kube.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	return newFallbackExecutor(req.URL(), res)
}

// newFallbackExecutor is split out so the transport choice is testable and the
// handler above stays readable.
func newFallbackExecutor(u *url.URL, res *resolved) (remotecommand.Executor, error) {
	spdy, err := remotecommand.NewSPDYExecutor(res.clients.Rest, "POST", u)
	if err != nil {
		return nil, fmt.Errorf("build exec transport: %w", err)
	}
	websocketExec, err := remotecommand.NewWebSocketExecutor(res.clients.Rest, "GET", u.String())
	if err != nil {
		// An older cluster without the WebSocket subprotocol still works.
		return spdy, nil
	}
	return remotecommand.NewFallbackExecutor(websocketExec, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
}
