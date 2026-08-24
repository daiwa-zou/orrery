package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/daiwazou/clusterlens/backend/internal/auth"
)

const (
	// writeWait bounds a single frame write so one wedged client cannot pin a
	// goroutine forever.
	writeWait = 10 * time.Second
	// pongWait must exceed pingPeriod; a client that misses two pings is gone.
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second

	maxClientMessage = 1 << 20
)

// upgrader validates the handshake. A WebSocket handshake carries cookies but
// cannot carry a CSRF header, so the Origin check is the cross-site defence.
func (a *API) upgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// Compression pays for itself on log and watch streams, which are
		// highly repetitive text.
		EnableCompression: true,
		CheckOrigin: func(r *http.Request) bool {
			return auth.OriginAllowed(r.Header.Get("Origin"), a.cfg.Server.PublicURL, a.cfg.Server.CORSOrigins)
		},
	}
}

// wsConn serialises writes from several goroutines onto one socket, which the
// gorilla API requires.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex

	closeOnce sync.Once
	done      chan struct{}
}

func newWSConn(c *websocket.Conn) *wsConn {
	w := &wsConn{conn: c, done: make(chan struct{})}
	_ = c.SetReadDeadline(time.Now().Add(pongWait))
	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(pongWait))
	})
	c.SetReadLimit(maxClientMessage)
	return w
}

// Done is closed when the peer goes away.
func (w *wsConn) Done() <-chan struct{} { return w.done }

func (w *wsConn) close() {
	w.closeOnce.Do(func() {
		close(w.done)
		_ = w.conn.Close()
	})
}

// WriteJSON sends one JSON frame.
func (w *wsConn) WriteJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.write(websocket.TextMessage, raw)
}

func (w *wsConn) write(messageType int, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return w.conn.WriteMessage(messageType, payload)
}

// ping keeps the connection alive through idle proxies and detects peers that
// have vanished without a close frame.
func (w *wsConn) ping(ctx context.Context) {
	t := time.NewTicker(pingPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case <-t.C:
			if err := w.write(websocket.PingMessage, nil); err != nil {
				w.close()
				return
			}
		}
	}
}

// drain reads and discards client frames so that control frames (pong, close)
// are processed. Handlers that expect client input supply their own reader.
func (w *wsConn) drain() {
	defer w.close()
	for {
		if _, _, err := w.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// closeWith sends a close frame carrying a human-readable reason.
func (w *wsConn) closeWith(code int, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	_ = w.write(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
	w.close()
}

// wsError reports a problem to the client in the stream's own JSON envelope
// before closing, so the UI can show why a stream ended.
func (w *wsConn) wsError(message string) {
	_ = w.WriteJSON(map[string]any{"type": "ERROR", "message": message})
	w.closeWith(websocket.CloseInternalServerErr, message)
}
